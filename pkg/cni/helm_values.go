package cni

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/deviceplugin"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/k8s"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	frrK8sNamespace       = "frr-k8s-system"
	frrK8sTokenSecretName = "frr-k8s-daemon-sa-for-dpu"
)

func (m *CNIManager) GenerateOVNKubernetesHelmValues(clusterName string, requireHostCredentials bool) error {
	mode := m.detectOVNKMode(clusterName)
	ovnImage := m.config.OvnKubernetesImageForHelm(DefaultOVNImage)
	return m.writeOVNKubernetesHelmValues(mode, clusterName, ovnImage, !requireHostCredentials)
}

func (m *CNIManager) writeOVNKubernetesHelmValues(mode ovnkMode, clusterName, ovnImage string, allowMissingHostCredentials bool) error {
	values := map[string]any{
		"global": map[string]any{},
	}
	global := values["global"].(map[string]any)
	tags := map[string]any{}

	imageRepo, imageTag := splitImageRef(ovnImage)
	pullPolicy := "Always"
	if m.config.IsKindMode() && !m.config.IsRegistryEnabled() {
		pullPolicy = "IfNotPresent"
	}

	switch mode {
	case ovnkModeFull:
		setImageValues(global, "image", imageRepo, imageTag, pullPolicy)
	case ovnkModeDPUHost:
		setImageValues(global, "image", imageRepo, imageTag, pullPolicy)
		tags["ovs-node"] = false
		tags["ovnkube-node-dpu-host"] = true
		tags["ovnkube-identity"] = false
		global["enableOvnKubeIdentity"] = false
		global["simulateDpu"] = true
		global["gatewayOpts"] = fmt.Sprintf("--gateway-interface=%s", m.config.GatewayInterfaces(clusterName))
		values["ovnkube-node-dpu-host"] = map[string]any{
			"nodeMgmtPortNetdev":     m.config.DPUHostManagementPortNetDevName(),
			"mgmtPortVFResourceName": deviceplugin.VFResourceName,
			"mgmtPortVFsCount":       1,
			"gatewayOpts":            fmt.Sprintf("--gateway-interface=%s", m.config.DPUHostGatewayInterface()),
		}
	case ovnkModeDPU:
		setImageValues(global, "dpuImage", imageRepo, imageTag, pullPolicy)
		tags["ovs-node"] = false
		tags["ovnkube-identity"] = false
		global["enableOvnKubeIdentity"] = false
		global["simulateDpu"] = true
		global["gatewayOpts"] = fmt.Sprintf("--gateway-interface=%s", m.config.GatewayInterfaces(clusterName))
		global["dpuHostGatewayRepresentorInterface"] = m.config.DPUHostGatewayRepresentorInterface()
		global["mtu"] = 1400

		creds, err := m.getDPUHostClusterCredentials()
		if err != nil {
			if !allowMissingHostCredentials {
				return fmt.Errorf("failed to get DPU host cluster credentials: %w", err)
			}
			log.Warn("DPU host cluster credentials are not available yet; generated %s values with empty host credentials: %v", mode, err)
		} else {
			global["dpuHostClusterK8sAPIServer"] = creds.APIServer
			global["dpuHostClusterK8sToken"] = creds.Token
			global["dpuHostClusterK8sCACertData"] = creds.CACert
			global["dpuHostClusterNetworkCIDR"] = creds.PodCIDR
			global["dpuHostClusterServiceCIDR"] = creds.ServiceCIDR
		}

		if err := m.writeFRRK8sRemoteArtifacts(clusterName); err != nil {
			if !allowMissingHostCredentials {
				return err
			}
			log.Warn("FRR-K8S host credentials are not available yet; rerun values generation after host FRR API resources are installed: %v", err)
		}
	}

	if len(tags) > 0 {
		values["tags"] = tags
	}

	path, err := m.ovnkValuesPath(clusterName, mode)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(values)
	if err != nil {
		return fmt.Errorf("failed to marshal OVN-Kubernetes values for cluster %s: %w", clusterName, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write OVN-Kubernetes values %s: %w", path, err)
	}

	log.Info("✓ Wrote OVN-Kubernetes Helm values for cluster %s: %s", clusterName, path)
	return nil
}

func setImageValues(global map[string]any, key, repository, tag, pullPolicy string) {
	global[key] = map[string]any{
		"repository": repository,
		"tag":        tag,
		"pullPolicy": pullPolicy,
	}
}

func (m *CNIManager) ovnkValuesPath(clusterName string, mode ovnkMode) (string, error) {
	dir, err := m.helmArtifactsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%s-ovn-kubernetes-%s-values.yaml", clusterName, mode.String())), nil
}

func (m *CNIManager) helmArtifactsDir() (string, error) {
	dir := filepath.Join(m.config.Kubernetes.GetKubeconfigDir(), "helm-values")
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve Helm artifacts directory %s: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("failed to create Helm artifacts directory %s: %w", abs, err)
	}
	return abs, nil
}

func (m *CNIManager) writeFRRK8sRemoteArtifacts(dpuClusterName string) error {
	creds, err := m.getDPUHostClusterSecretCredentials(frrK8sNamespace, frrK8sTokenSecretName)
	if err != nil {
		return fmt.Errorf("failed to get FRR-K8S host cluster credentials: %w", err)
	}

	dir, err := m.helmArtifactsDir()
	if err != nil {
		return err
	}
	kubeconfigPath := filepath.Join(dir, fmt.Sprintf("%s-frr-k8s-host.kubeconfig", dpuClusterName))
	if err := os.WriteFile(kubeconfigPath, []byte(renderHostKubeconfig(creds, "frr-k8s-host")), 0o600); err != nil {
		return fmt.Errorf("failed to write FRR-K8S host kubeconfig %s: %w", kubeconfigPath, err)
	}

	hostClusterName := m.config.GetDPUHostClusterName()
	hostKubeconfigPath := k8s.GetKubeconfigPath(hostClusterName, m.config.Kubernetes.GetKubeconfigDir())
	hostKubeconfigPath, err = filepath.Abs(hostKubeconfigPath)
	if err != nil {
		return fmt.Errorf("failed to resolve host kubeconfig path %s: %w", hostKubeconfigPath, err)
	}
	if err := m.writeFRRK8sRemoteEnv(dpuClusterName, kubeconfigPath, hostKubeconfigPath); err != nil {
		return err
	}

	log.Info("✓ Wrote FRR-K8S host kubeconfig for DPU cluster %s: %s", dpuClusterName, kubeconfigPath)
	return nil
}

func (m *CNIManager) writeFRRK8sRemoteEnv(dpuClusterName, remoteKubeconfigPath, hostKubeconfigPath string) error {
	pairs := m.config.GetHostDPUPairs(dpuClusterName)
	if len(pairs) == 0 {
		return fmt.Errorf("no host/DPU node pairs found for DPU cluster %s", dpuClusterName)
	}

	entries := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		entries = append(entries, fmt.Sprintf("%s=%s", pair.HostNode, pair.DPUNode))
	}

	dir, err := m.helmArtifactsDir()
	if err != nil {
		return err
	}
	envPath := filepath.Join(dir, fmt.Sprintf("%s-frr-k8s.env", dpuClusterName))
	env := fmt.Sprintf("FRR_K8S_REMOTE_KUBECONFIG=%q\nFRR_K8S_HOST_KUBECONFIG=%q\nFRR_K8S_REMOTE_NODE_MAP=%q\n", remoteKubeconfigPath, hostKubeconfigPath, strings.Join(entries, ","))
	if err := os.WriteFile(envPath, []byte(env), 0o644); err != nil {
		return fmt.Errorf("failed to write FRR-K8S env file %s: %w", envPath, err)
	}

	log.Info("✓ Wrote FRR-K8S remote install environment for DPU cluster %s: %s", dpuClusterName, envPath)
	return nil
}

func renderHostKubeconfig(creds *DPUHostCredentials, name string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: %s
  cluster:
    server: %s
    certificate-authority-data: %s
users:
- name: %s
  user:
    token: %s
contexts:
- name: %s
  context:
    cluster: %s
    user: %s
current-context: %s
`, name, creds.APIServer, creds.CACert, name, creds.Token, name, name, name, name)
}

func (m *CNIManager) getDPUHostClusterSecretCredentials(namespace, secretName string) (*DPUHostCredentials, error) {
	hostClusterName := m.config.GetDPUHostClusterName()
	hostKubeconfigPath := k8s.GetKubeconfigPath(hostClusterName, m.config.Kubernetes.GetKubeconfigDir())
	log.Info("Retrieving host cluster credentials from %s secret %s/%s...", hostKubeconfigPath, namespace, secretName)

	hostClient, err := k8s.NewClientFromFile(hostKubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create host cluster client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	secret, err := hostClient.Clientset().CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get host cluster secret %s/%s: %w", namespace, secretName, err)
	}

	tokenBytes, ok := secret.Data["token"]
	if !ok {
		return nil, fmt.Errorf("token not found in host cluster secret %s/%s", namespace, secretName)
	}
	caCertBytes, ok := secret.Data["ca.crt"]
	if !ok {
		return nil, fmt.Errorf("ca.crt not found in host cluster secret %s/%s", namespace, secretName)
	}

	hostClusterCfg := m.config.GetClusterConfig(hostClusterName)
	if hostClusterCfg == nil {
		return nil, fmt.Errorf("host cluster %s not found in config", hostClusterName)
	}

	apiServerURL := ""
	if m.config.IsVMMode() {
		masterVMs := m.config.GetClusterRoleMapping()[hostClusterName][config.ClusterRoleMaster]
		if len(masterVMs) > 0 {
			apiServerURL = "https://" + masterVMs[0].K8sNodeIP + ":6443"
		}
	} else if m.config.IsKindMode() {
		ip, err := m.getKindControlPlaneIP(hostClusterName)
		if err != nil {
			return nil, fmt.Errorf("failed to get Kind control plane IP for host cluster %s: %w", hostClusterName, err)
		}
		apiServerURL = "https://" + ip + ":6443"
	}
	if apiServerURL == "" {
		apiServerURL = hostClient.GetAPIServerURL()
	}

	podCIDR := hostClusterCfg.PodCIDR
	if !strings.Contains(podCIDR, "/24") || strings.Count(podCIDR, "/") < 2 {
		podCIDR += "/24"
	}

	return &DPUHostCredentials{
		APIServer:   apiServerURL,
		Token:       string(tokenBytes),
		CACert:      base64.StdEncoding.EncodeToString(caCertBytes),
		PodCIDR:     podCIDR,
		ServiceCIDR: hostClusterCfg.ServiceCIDR,
	}, nil
}
