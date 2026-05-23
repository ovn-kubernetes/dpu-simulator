package cni

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
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

type helmValue struct {
	key   string
	value any
}

func (m *CNIManager) GenerateOVNKubernetesHelmValues(clusterName string, requireHostCredentials bool) error {
	mode := m.detectOVNKMode(clusterName)
	ovnImage := m.config.OvnKubernetesImageForHelm(DefaultOVNImage)
	return m.writeOVNKubernetesHelmValues(mode, clusterName, ovnImage, requireHostCredentials)
}

func (m *CNIManager) writeOVNKubernetesHelmValues(mode ovnkMode, clusterName, ovnImage string, requireHostCredentials bool) error {
	overrides, err := m.ovnkHelmOverrides(mode, clusterName, ovnImage, requireHostCredentials, true)
	if err != nil {
		return err
	}
	values, err := helmValuesToMap(overrides)
	if err != nil {
		return err
	}
	if mode == ovnkModeDPU {
		if err := m.writeFRRK8sRemoteArtifacts(clusterName); err != nil {
			if requireHostCredentials {
				return err
			}
			log.Warn("FRR-K8S host credentials are not available yet; rerun values generation after host FRR API resources are installed: %v", err)
		}
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

// ovnkHelmOverrides returns the DPU-simulator-specific Helm overrides.
// includeDPUHostMgmtPortResource is only needed when the rendered chart will
// request management-port VFs, such as manual primary UDN installs.
func (m *CNIManager) ovnkHelmOverrides(mode ovnkMode, clusterName, ovnImage string, requireHostCredentials, includeDPUHostMgmtPortResource bool) ([]helmValue, error) {
	imageRepo, imageTag := splitImageRef(ovnImage)
	pullPolicy := "Always"
	if m.config.IsKindMode() && !m.config.IsRegistryEnabled() {
		pullPolicy = "IfNotPresent"
	}

	switch mode {
	case ovnkModeFull:
		return imageHelmValues("global.image", imageRepo, imageTag, pullPolicy), nil
	case ovnkModeDPUHost:
		overrides := imageHelmValues("global.image", imageRepo, imageTag, pullPolicy)
		overrides = append(overrides,
			helmValue{key: "tags.ovs-node", value: false},
			helmValue{key: "tags.ovnkube-node-dpu-host", value: true},
			helmValue{key: "tags.ovnkube-identity", value: false},
			helmValue{key: "global.enableOvnKubeIdentity", value: false},
			helmValue{key: "global.simulateDpu", value: true},
			helmValue{key: "global.gatewayOpts", value: m.config.GatewayOpts(clusterName)},
			helmValue{key: "ovnkube-node-dpu-host.nodeMgmtPortNetdev", value: m.config.DPUHostManagementPortNetDevName()},
			helmValue{key: "ovnkube-node-dpu-host.gatewayOpts", value: fmt.Sprintf("--gateway-interface=%s", m.config.DPUHostGatewayInterface())},
		)
		if includeDPUHostMgmtPortResource {
			overrides = append(overrides,
				helmValue{key: "ovnkube-node-dpu-host.mgmtPortVFResourceName", value: deviceplugin.VFResourceName},
				helmValue{key: "ovnkube-node-dpu-host.mgmtPortVFsCount", value: m.config.DPUHostManagementPortVFsCount()},
			)
		}
		return overrides, nil
	case ovnkModeDPU:
		overrides := imageHelmValues("global.dpuImage", imageRepo, imageTag, pullPolicy)
		overrides = append(overrides,
			helmValue{key: "tags.ovs-node", value: false},
			helmValue{key: "tags.ovnkube-identity", value: false},
			helmValue{key: "global.enableOvnKubeIdentity", value: false},
			helmValue{key: "global.simulateDpu", value: true},
			helmValue{key: "global.gatewayOpts", value: m.config.GatewayOpts(clusterName)},
			helmValue{key: "global.dpuHostGatewayRepresentorInterface", value: m.config.DPUHostGatewayRepresentorInterface()},
			helmValue{key: "global.mtu", value: 1400},
		)

		creds, err := m.getDPUHostClusterCredentials()
		if err != nil {
			if requireHostCredentials {
				return nil, fmt.Errorf("failed to get DPU host cluster credentials: %w", err)
			}
			log.Warn("DPU host cluster credentials are not available yet; generated %s values with empty host credentials: %v", mode, err)
			return overrides, nil
		}

		overrides = append(overrides,
			helmValue{key: "global.dpuHostClusterK8sAPIServer", value: creds.APIServer},
			helmValue{key: "global.dpuHostClusterK8sToken", value: creds.Token},
			helmValue{key: "global.dpuHostClusterK8sCACertData", value: creds.CACert},
			helmValue{key: "global.dpuHostClusterNetworkCIDR", value: creds.PodCIDR},
			helmValue{key: "global.dpuHostClusterServiceCIDR", value: creds.ServiceCIDR},
		)
		return overrides, nil
	default:
		return nil, fmt.Errorf("unsupported OVN-Kubernetes mode %q", mode)
	}
}

func imageHelmValues(prefix, repository, tag, pullPolicy string) []helmValue {
	return []helmValue{
		{key: prefix + ".repository", value: repository},
		{key: prefix + ".tag", value: tag},
		{key: prefix + ".pullPolicy", value: pullPolicy},
	}
}

func appendHelmSetArgs(args []string, values []helmValue) []string {
	for _, value := range values {
		args = append(args, "--set", fmt.Sprintf("%s=%v", value.key, value.value))
	}
	return args
}

func helmValuesToMap(values []helmValue) (map[string]any, error) {
	result := map[string]any{}
	for _, value := range values {
		if err := setNestedHelmValue(result, value.key, value.value); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func setNestedHelmValue(values map[string]any, key string, value any) error {
	parts := strings.Split(key, ".")
	if len(parts) == 0 || parts[0] == "" {
		return fmt.Errorf("invalid empty Helm value key")
	}

	current := values
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part]
		if !ok {
			child := map[string]any{}
			current[part] = child
			current = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("helm value key %q conflicts with scalar value at %q", key, part)
		}
		current = child
	}

	last := parts[len(parts)-1]
	if last == "" {
		return fmt.Errorf("invalid Helm value key %q", key)
	}
	current[last] = value
	return nil
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
	env := fmt.Sprintf("FRR_K8S_REMOTE_KUBECONFIG=%q\nFRR_K8S_HOST_KUBECONFIG=%q\nFRR_K8S_REMOTE_NODE_MAP=%q\nDPU_SIM_GATEWAY_NETWORK=%q\nDPU_SIM_GATEWAY_SUBNET=%q\n",
		remoteKubeconfigPath, hostKubeconfigPath, strings.Join(entries, ","), m.config.DPUKindGatewayNetworkName(), m.config.DPUHostGatewaySubnet())
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
		apiServerURL = kindControlPlaneAPIURL(hostClusterName)
	}
	if apiServerURL == "" {
		apiServerURL = hostClient.GetAPIServerURL()
	}

	podCIDR, err := podCIDRWithPerNodePrefix(hostClusterCfg.PodCIDR)
	if err != nil {
		return nil, err
	}

	return &DPUHostCredentials{
		APIServer:   apiServerURL,
		Token:       string(tokenBytes),
		CACert:      base64.StdEncoding.EncodeToString(caCertBytes),
		PodCIDR:     podCIDR,
		ServiceCIDR: hostClusterCfg.ServiceCIDR,
	}, nil
}

func podCIDRWithPerNodePrefix(podCIDR string) (string, error) {
	parts := strings.Split(podCIDR, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return "", fmt.Errorf("pod CIDR %q must be CIDR or CIDR/hostSubnetPrefix", podCIDR)
	}

	_, parsedCIDR, err := net.ParseCIDR(strings.Join(parts[:2], "/"))
	if err != nil {
		return "", fmt.Errorf("failed to parse pod CIDR %q: %w", podCIDR, err)
	}
	if parsedCIDR.IP.To4() == nil {
		return "", fmt.Errorf("pod CIDR %q must be IPv4", podCIDR)
	}
	clusterPrefix, _ := parsedCIDR.Mask.Size()

	hostPrefix := 24
	if len(parts) == 3 {
		hostPrefix, err = strconv.Atoi(parts[2])
		if err != nil {
			return "", fmt.Errorf("failed to parse host subnet prefix in pod CIDR %q: %w", podCIDR, err)
		}
	}
	if hostPrefix <= clusterPrefix || hostPrefix > 32 {
		return "", fmt.Errorf("pod CIDR %q must use a host subnet prefix greater than the cluster prefix and no larger than 32", podCIDR)
	}

	if len(parts) == 3 {
		return podCIDR, nil
	}
	return fmt.Sprintf("%s/%d", podCIDR, hostPrefix), nil
}
