package cni

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/containerengine"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/deviceplugin"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/k8s"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

const (
	// DefaultOVNImage is the default OVN-Kubernetes image
	DefaultOVNImage = "ghcr.io/ovn-kubernetes/ovn-kubernetes/ovn-kube-fedora:master"

	// DefaultOVNRepoURL is the default URL for the OVN-Kubernetes repository
	DefaultOVNRepoURL = "https://github.com/ovn-org/ovn-kubernetes.git"
)

// ovnkMode describes how OVN-Kubernetes is deployed in a cluster.
type ovnkMode int

const (
	ovnkModeFull    ovnkMode = iota // every node runs OVS+OVN (interconnect)
	ovnkModeDPUHost                 // host cluster in DPU-offload topology
	ovnkModeDPU                     // DPU cluster in DPU-offload topology
)

func (m ovnkMode) String() string {
	switch m {
	case ovnkModeDPUHost:
		return "dpu-host"
	case ovnkModeDPU:
		return "dpu"
	default:
		return "full"
	}
}

// detectOVNKMode returns the deployment mode for the given cluster based on
// config. When offload_dpu is false the mode is always full. Otherwise the
// cluster that contains DPU-type nodes is the DPU cluster and the other is the
// DPU-host cluster.
// TODO: Single cluster support for DPU + DPU-host is not yet supported.
func (m *CNIManager) detectOVNKMode(clusterName string) ovnkMode {
	if !m.config.IsOffloadDPU() {
		return ovnkModeFull
	}
	if m.config.IsDPUCluster(clusterName) {
		return ovnkModeDPU
	}
	return ovnkModeDPUHost
}

// patchCoreDNSForOVN patches the CoreDNS configmap for OVN-Kubernetes compatibility.
// This modifies CoreDNS to:
// 1. Work in an offline environment (no IPv6 connectivity)
// 2. Handle additional domains (like .net.) and return NXDOMAIN instead of SERVFAIL
// 3. Remove problematic directives like 'loop', 'upstream', and 'fallthrough' from CoreDNS config
func (m *CNIManager) patchCoreDNS(dnsServer string) error {
	log.Info("Patching CoreDNS configmap for OVN-Kubernetes compatibility, dns server: %s", dnsServer)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get the current CoreDNS configmap
	configMap, err := m.k8sClient.Clientset().CoreV1().ConfigMaps("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get CoreDNS configmap: %w", err)
	}

	// Get the Corefile content
	corefile, ok := configMap.Data["Corefile"]
	if !ok {
		return fmt.Errorf("corefile not found in CoreDNS configmap")
	}

	// Apply patches to the Corefile
	var patchedLines []string
	for _, line := range strings.Split(corefile, "\n") {
		// Skip lines containing 'upstream', 'fallthrough', or 'loop'
		// These are problematic lines that need to be removed
		if regexp.MustCompile(`^\s*upstream\s*$`).MatchString(line) {
			continue
		}
		if regexp.MustCompile(`^\s*fallthrough.*$`).MatchString(line) {
			continue
		}
		if regexp.MustCompile(`^\s*loop\s*$`).MatchString(line) {
			continue
		}

		// Add 'net' after 'kubernetes cluster.local' to handle .net. domain
		line = regexp.MustCompile(`^(\s*kubernetes cluster\.local)`).ReplaceAllString(line, "${1} net")

		// Replace forward line to use specified DNS server
		line = regexp.MustCompile(`^(\s*forward \.)\s*.*$`).ReplaceAllString(line, fmt.Sprintf("${1} %s {", dnsServer))

		patchedLines = append(patchedLines, line)
	}

	patchedCorefile := strings.Join(patchedLines, "\n")

	// Update the configmap with the patched Corefile
	configMap.Data["Corefile"] = patchedCorefile

	_, err = m.k8sClient.Clientset().CoreV1().ConfigMaps("kube-system").Update(ctx, configMap, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update CoreDNS configmap: %w", err)
	}

	log.Info("✓ CoreDNS configmap patched successfully")
	return nil
}

// installOVNKubernetes installs OVN-Kubernetes CNI using Helm charts.
// The deployment topology (full / DPU-host / DPU) is determined automatically
// from the config.
func (m *CNIManager) installOVNKubernetes(clusterName string, k8sIP string) error {
	mode := m.detectOVNKMode(clusterName)

	clusterCfg := m.config.GetClusterConfig(clusterName)
	if clusterCfg == nil {
		return fmt.Errorf("cluster configuration not found for cluster %s", clusterName)
	}
	podCIDR := clusterCfg.PodCIDR
	serviceCIDR := clusterCfg.ServiceCIDR
	apiServerURL := "https://" + k8sIP + ":6443"

	log.Info("Installing OVN-Kubernetes (mode=%s): Pod CIDR: %s, Service CIDR: %s, API Server: %s",
		mode, podCIDR, serviceCIDR, apiServerURL)

	if err := m.patchCoreDNS("8.8.8.8"); err != nil {
		return fmt.Errorf("failed to patch CoreDNS: %w", err)
	}

	ovnKPath, err := EnsureOVNKubernetesSource(m.cmdExec, m.config.OVNKubernetesPath)
	if err != nil {
		return fmt.Errorf("failed to ensure OVN-Kubernetes source: %w", err)
	}

	regContainer := m.config.GetRegistryContainerForCNI(config.CNIOVNKubernetes)
	needsInstallTimeOVNKBuild := regContainer != nil &&
		!m.config.IsRegistryEnabled() &&
		!(m.config.IsRegistryImageBuildOnly() && m.config.IsKindMode())
	if needsInstallTimeOVNKBuild {
		engine, err := containerengine.NewProjectEngine(m.cmdExec)
		if err != nil {
			return fmt.Errorf("failed to create project engine: %w", err)
		}
		if err := BuildOVNKubernetesImageWithEngine(m.config, m.cmdExec, engine, regContainer.Tag, ""); err != nil {
			return fmt.Errorf("failed to build OVN-Kubernetes image: %w", err)
		}
	}

	dpImageRef := deviceplugin.DefaultDevicePluginImage
	if m.config.IsOffloadDPU() {
		switch {
		case m.config.IsRegistryEnabled():
			dpImageRef = m.config.GetRegistryImageRef(deviceplugin.DevicePluginImage)
		case m.config.IsRegistryImageBuildOnly() && m.config.IsKindMode():
			dpImageRef = config.KindNodeLocalImageRef(deviceplugin.DevicePluginImage)
		}
	}

	ovnImage := m.config.OvnKubernetesImageForHelm(DefaultOVNImage)
	if regContainer != nil {
		log.Info("OVN-Kubernetes Helm image: %s", ovnImage)
	}

	if err := m.labelNodesForSingleNodeZones(); err != nil {
		return fmt.Errorf("failed to label nodes for zones: %w", err)
	}

	if err := m.labelOVNMasterNodes(clusterName); err != nil {
		return fmt.Errorf("failed to label master nodes: %w", err)
	}

	switch mode {
	case ovnkModeDPUHost:
		if err := m.labelNodesForDPUHost(clusterName); err != nil {
			return fmt.Errorf("failed to label DPU-host nodes: %w", err)
		}
		if err := deviceplugin.DeployDevicePlugin(m.k8sClient, dpImageRef); err != nil {
			return fmt.Errorf("failed to deploy device plugin on DPU-host cluster: %w", err)
		}
	case ovnkModeDPU:
		if err := m.labelNodesForDPU(clusterName); err != nil {
			return fmt.Errorf("failed to label DPU nodes: %w", err)
		}
	}

	if !m.config.ShouldInstallOVNKubernetes() {
		if err := m.writeOVNKubernetesHelmValues(mode, clusterName, ovnImage, false); err != nil {
			return fmt.Errorf("failed to write OVN-Kubernetes Helm values: %w", err)
		}
		log.Info("✓ OVN-Kubernetes Helm install skipped for cluster %s (mode=%s); generated values instead", clusterName, mode)
		return nil
	}

	if err := m.runHelmInstall(mode, clusterName, ovnKPath, apiServerURL, podCIDR, serviceCIDR, ovnImage); err != nil {
		return fmt.Errorf("failed to run helm install: %w", err)
	}

	if err := m.installExternalCRDs(); err != nil {
		return fmt.Errorf("failed to install external CRDs: %w", err)
	}

	if err := m.k8sClient.WaitForPodsReady("ovn-kubernetes", "", 5*time.Minute); err != nil {
		log.Warn("Warning: OVN-Kubernetes pods may not be ready: %v", err)
	} else {
		log.Info("✓ OVN-Kubernetes pods are ready, installed via Helm successfully (mode=%s)", mode)
	}

	// In DPU mode the primary cluster CNI (e.g. flannel) relies on
	// kube-proxy for service routing, so only delete it when OVN-K
	// handles services itself (full and dpu-host modes).
	if mode != ovnkModeDPU {
		if err := m.k8sClient.DeleteDaemonSet("kube-system", "kube-proxy"); err != nil {
			return fmt.Errorf("failed to delete kube-proxy DaemonSet: %w", err)
		}
	}

	// DPU-host: create the service account secret for cross-cluster access
	if mode == ovnkModeDPUHost {
		if err := m.createDPUAccessSecret(); err != nil {
			return fmt.Errorf("failed to create DPU access secret: %w", err)
		}
	}

	return nil
}

// runHelmInstall executes `helm install` for OVN-Kubernetes. The values file,
// image key, and additional flags are selected based on the deployment mode:
//   - Full:     values-single-node-zone.yaml, global.image.*, OvnKubeIdentity
//   - DPU-Host: values-single-node-zone.yaml, global.image.*, dpu-host subchart
//   - DPU:      values-single-node-zone-dpu.yaml, global.dpuImage.*, host credentials
func (m *CNIManager) runHelmInstall(mode ovnkMode, clusterName, ovnkRepoPath, apiServerURL, podCIDR, serviceCIDR, ovnImage string) error {
	chartPath := filepath.Join(ovnkRepoPath, "helm", "ovn-kubernetes")
	// Embedded installs do not enable network segmentation, so keep the DPU
	// host on the explicit management netdev instead of requesting VFs.
	overrides, err := m.ovnkHelmOverrides(mode, clusterName, ovnImage, true, false)
	if err != nil {
		return err
	}

	args := []string{"install", "ovn-kubernetes", "."}

	switch mode {
	case ovnkModeFull, ovnkModeDPUHost:
		args = append(args,
			"-f", "values-single-node-zone.yaml",
			"--set", fmt.Sprintf("k8sAPIServer=%s", apiServerURL),
			"--set", fmt.Sprintf("podNetwork=%s", podCIDR),
			"--set", fmt.Sprintf("serviceNetwork=%s", serviceCIDR),
			"--set", "mtu=1400",
			"--set", "global.enableAdminNetworkPolicy=true",
			"--set", "global.enableEgressIp=true",
			"--set", "global.egressIpHealthCheckPort=9107",
			"--set", "global.enableEgressFirewall=true",
			"--set", "global.enableEgressQos=true",
			"--set", "global.enableEgressService=true",
			"--set", "global.enableMultiExternalGateway=true",
			"--set", "global.enablePersistentIPs=true",
		)

		if mode == ovnkModeFull {
			overrides = append(overrides,
				helmValue{key: "tags.ovs-node", value: false},
				helmValue{key: "global.enableOvnKubeIdentity", value: true},
				helmValue{key: "global.gatewayOpts", value: m.config.GatewayOpts(clusterName)},
			)
		}
	case ovnkModeDPU:
		args = append(args,
			"-f", "values-single-node-zone-dpu.yaml",
		)
	}
	args = appendHelmSetArgs(args, overrides)

	if m.kubeconfigPath != "" {
		args = append(args, "--kubeconfig", m.kubeconfigPath)
	}

	log.Info("Running Helm install for OVN-Kubernetes (mode=%s, chart: %s)...", mode, chartPath)
	log.Debug("Helm args: %v", args)

	stdout, stderr, err := platform.RunCommandInDir(m.cmdExec, chartPath, "helm", args, 45*time.Minute)
	output := platform.CombinedCmdOutput(stdout, stderr)
	if err != nil {
		return fmt.Errorf("helm install failed (mode=%s): %w\n%s", mode, err, output)
	}

	log.Info("✓ Helm install completed successfully (mode=%s)", mode)
	log.Debug("Helm output: %s", output)
	return nil
}

// labelNodesForSingleNodeZones labels each node with its own zone name for
// interconnect mode. In single-node-zone IC, every node is its own zone,
// matching the label_ovn_single_node_zones.
func (m *CNIManager) labelNodesForSingleNodeZones() error {
	log.Info("Labeling nodes for single-node-zone interconnect...")

	nodes, err := m.k8sClient.GetNodes()
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	for _, node := range nodes {
		labels := map[string]string{
			"k8s.ovn.org/zone-name": node.Name,
		}
		if err := m.k8sClient.LabelNode(node.Name, labels); err != nil {
			return fmt.Errorf("failed to label node %s with zone: %w", node.Name, err)
		}
		log.Debug("✓ Labeled node %s with zone %s", node.Name, node.Name)
	}

	log.Info("✓ All nodes labeled for single-node-zone interconnect")
	return nil
}

// installExternalCRDs applies the external ANP and BANP CRDs required by
// OVN-Kubernetes. These are not bundled in the Helm chart and must be applied
// separately. Matches install_online_ovn_kubernetes_crds in kind-helm.sh.
func (m *CNIManager) installExternalCRDs() error {
	log.Info("Applying external CRD manifests (ANP/BANP)...")

	externalCRDs := []string{
		"https://raw.githubusercontent.com/kubernetes-sigs/network-policy-api/v0.1.5/config/crd/experimental/policy.networking.k8s.io_adminnetworkpolicies.yaml",
		"https://raw.githubusercontent.com/kubernetes-sigs/network-policy-api/v0.1.5/config/crd/experimental/policy.networking.k8s.io_baselineadminnetworkpolicies.yaml",
	}

	for _, url := range externalCRDs {
		if err := m.k8sClient.ApplyManifestFromURL(url); err != nil {
			return fmt.Errorf("failed to apply external CRD from %s: %w", url, err)
		}
		log.Debug("✓ Applied external CRD from %s", url)
	}

	m.k8sClient.InvalidateDiscoveryCache()
	log.Info("✓ External CRDs applied successfully")
	return nil
}

// redeployOVNKubernetes re-deploys OVN-Kubernetes by running helm upgrade
// with --reuse-values so the original install configuration is preserved.
// Only the image is updated to pick up the newly built container.
func (m *CNIManager) redeployOVNKubernetes(clusterName string) error {
	log.Info("Redeploying OVN-Kubernetes via Helm on cluster %s...", clusterName)

	ovnKPath, err := EnsureOVNKubernetesSource(m.cmdExec, m.config.OVNKubernetesPath)
	if err != nil {
		return fmt.Errorf("failed to ensure OVN-Kubernetes source: %w", err)
	}

	chartPath := filepath.Join(ovnKPath, "helm", "ovn-kubernetes")

	ovnImage := m.config.OvnKubernetesImageForHelm(DefaultOVNImage)
	imageRepo, imageTag := splitImageRef(ovnImage)

	mode := m.detectOVNKMode(clusterName)
	args := []string{
		"upgrade", "ovn-kubernetes", ".",
		"--reuse-values",
	}
	switch mode {
	case ovnkModeDPU:
		args = append(args,
			"--set", fmt.Sprintf("global.dpuImage.repository=%s", imageRepo),
			"--set", fmt.Sprintf("global.dpuImage.tag=%s", imageTag),
		)
	default:
		args = append(args,
			"--set", fmt.Sprintf("global.image.repository=%s", imageRepo),
			"--set", fmt.Sprintf("global.image.tag=%s", imageTag),
		)
	}

	if m.kubeconfigPath != "" {
		args = append(args, "--kubeconfig", m.kubeconfigPath)
	}

	log.Info("Running helm upgrade for OVN-Kubernetes...")
	stdout, stderr, err := platform.RunCommandInDir(m.cmdExec, chartPath, "helm", args, 45*time.Minute)
	output := platform.CombinedCmdOutput(stdout, stderr)
	if err != nil {
		return fmt.Errorf("helm upgrade failed: %w\n%s", err, output)
	}

	log.Info("✓ Helm upgrade completed successfully")
	log.Debug("Helm upgrade output: %s", output)

	if err := m.k8sClient.WaitForPodsReady("ovn-kubernetes", "", 5*time.Minute); err != nil {
		log.Warn("Warning: OVN-Kubernetes pods may not be ready after Helm upgrade: %v", err)
	} else {
		log.Info("✓ OVN-Kubernetes pods are ready after Helm redeploy on cluster %s", clusterName)
	}

	return nil
}

// splitImageRef splits a container image reference (e.g.
// "ghcr.io/ovn-kubernetes/ovn-kube-fedora:master") into its repository and tag
// components. If no tag is present, "latest" is used.
func splitImageRef(image string) (repo, tag string) {
	lastColon := strings.LastIndex(image, ":")
	lastSlash := strings.LastIndex(image, "/")
	if lastColon > lastSlash && lastColon >= 0 {
		return image[:lastColon], image[lastColon+1:]
	}
	return image, "latest"
}

// labelOVNMasterNodes labels only the master nodes (from config) for OVN HA deployment
func (m *CNIManager) labelOVNMasterNodes(clusterName string) error {
	log.Debug("Labeling master nodes for OVN-Kubernetes HA...")

	masterVMs := m.config.GetClusterRoleMapping()[clusterName][config.ClusterRoleMaster]
	for _, vm := range masterVMs {
		labels := map[string]string{
			"k8s.ovn.org/ovnkube-db":                "true",
			"node-role.kubernetes.io/control-plane": "",
		}
		if err := m.k8sClient.LabelNode(vm.Name, labels); err != nil {
			return fmt.Errorf("failed to label node %s: %w", vm.Name, err)
		}
		log.Debug("✓ Labeled node %s", vm.Name)

		m.k8sClient.RemoveNodeTaint(vm.Name, "node-role.kubernetes.io/master", corev1.TaintEffectNoSchedule)
		m.k8sClient.RemoveNodeTaint(vm.Name, "node-role.kubernetes.io/control-plane", corev1.TaintEffectNoSchedule)
		log.Debug("✓ Removed taints from node %s", vm.Name)
	}

	log.Info("✓ Master nodes labeled for OVN-Kubernetes HA")
	return nil
}

// getOVNKubernetesPath returns the path to the ovn-kubernetes directory.
// When overridePath is non-empty it is used directly (absolute or resolved
// relative to cwd); otherwise the default <project-root>/ovn-kubernetes is
// returned.
func getOVNKubernetesPath(overridePath string) (string, error) {
	if overridePath != "" {
		absPath, err := filepath.Abs(overridePath)
		if err != nil {
			return "", fmt.Errorf("invalid --ovn-kubernetes-path value %q: %w", overridePath, err)
		}
		return absPath, nil
	}
	projectRoot, err := platform.GetProjectRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(projectRoot, "ovn-kubernetes"), nil
}

// isOVNKubernetesPopulated checks if the ovn-kubernetes directory contains actual content.
func isOVNKubernetesPopulated(cmdExec platform.CommandExecutor, ovnPath string) bool {
	helmChart := filepath.Join(ovnPath, "helm", "ovn-kubernetes", "Chart.yaml")
	exists, err := cmdExec.FileExists(helmChart)
	if err != nil {
		log.Error("Failed to check if Helm chart exists: %v", err)
		return false
	}
	return exists
}

// requireOVNKubernetesPath resolves a non-empty override path and
// returns an error if helm/ovn-kubernetes/Chart.yaml is missing.
func requireOVNKubernetesPath(cmdExec platform.CommandExecutor, overridePath string) (string, error) {
	ovnPath, err := getOVNKubernetesPath(overridePath)
	if err != nil {
		return "", err
	}
	if !isOVNKubernetesPopulated(cmdExec, ovnPath) {
		return "", fmt.Errorf("%q does not contain OVN-Kubernetes source (missing helm/ovn-kubernetes/Chart.yaml)", ovnPath)
	}
	return ovnPath, nil
}

// ValidateOVNKubernetesPath returns nil if overridePath is empty (default checkout / clone applies at deploy time).
// If overridePath is non-empty after trimming whitespace, it must contain helm/ovn-kubernetes/Chart.yaml.
func ValidateOVNKubernetesPath(overridePath string) error {
	overridePath = strings.TrimSpace(overridePath)
	if overridePath == "" {
		return nil
	}
	_, err := requireOVNKubernetesPath(platform.NewLocalExecutor(), overridePath)
	return err
}

// EnsureOVNKubernetesSource ensures the ovn-kubernetes source code is available.
// When overridePath is non-empty (set via --ovn-kubernetes-path) the directory
// is used as-is. Otherwise it checks the default <project-root>/ovn-kubernetes
// location and clones the repository if missing.
func EnsureOVNKubernetesSource(cmdExec platform.CommandExecutor, overridePath string) (string, error) {
	overridePath = strings.TrimSpace(overridePath)

	if overridePath != "" {
		ovnPath, err := requireOVNKubernetesPath(cmdExec, overridePath)
		if err != nil {
			return "", fmt.Errorf("--ovn-kubernetes-path: %w", err)
		}
		log.Info("Using OVN-Kubernetes source from --ovn-kubernetes-path=%s", ovnPath)
		return ovnPath, nil
	}

	ovnPath, err := getOVNKubernetesPath("")
	if err != nil {
		return "", fmt.Errorf("failed to get OVN-Kubernetes path: %w", err)
	}

	if isOVNKubernetesPopulated(cmdExec, ovnPath) {
		log.Debug("OVN-Kubernetes source found at %s", ovnPath)
		return ovnPath, nil
	}

	// Remove only an empty (or .git-only) leftover directory so clone can succeed.
	exists, err := cmdExec.FileExists(ovnPath)
	if err != nil {
		return "", fmt.Errorf("failed to check OVN-Kubernetes path: %w", err)
	}

	if exists {
		names, err := cmdExec.ReadDirNames(ovnPath)
		if err != nil {
			return "", fmt.Errorf("failed to read OVN-Kubernetes directory %s: %w", ovnPath, err)
		}
		meaningful := platform.MeaningfulDirEntries(names)
		if len(meaningful) == 0 {
			log.Info("OVN-Kubernetes directory exists but is empty (or only .git), removing before clone...")
			if err := cmdExec.RemoveAll(ovnPath); err != nil {
				return "", fmt.Errorf("failed to remove empty ovn-kubernetes directory: %w", err)
			}
		} else {
			helmChart := filepath.Join(ovnPath, "helm", "ovn-kubernetes", "Chart.yaml")
			return "", fmt.Errorf("OVN-Kubernetes directory %q exists but %s is missing (invalid or partial checkout; directory is not empty — remove or fix the tree and retry)", ovnPath, helmChart)
		}
	}

	log.Info("OVN-Kubernetes not found, cloning from %s:master...", DefaultOVNRepoURL)

	projectRoot, err := platform.GetProjectRoot()
	if err != nil {
		return "", fmt.Errorf("failed to get project root: %w", err)
	}

	if err := cmdExec.RunCmdInDir(log.LevelInfo, projectRoot, "git", "clone", "--branch", "master", DefaultOVNRepoURL, ovnPath); err != nil {
		return "", fmt.Errorf("failed to clone OVN-Kubernetes repository: %w", err)
	}

	log.Info("✓ OVN-Kubernetes cloned to %s", ovnPath)
	return ovnPath, nil
}

// BuildOVNKubernetesImage builds the OVN-Kubernetes container image from the local
// source code using the Dockerfile.fedora in ovn-kubernetes/dist/images/.
// imageName specifies the tag for the built image (e.g., "ovn-kube-fedora:latest").
// By default, OVN/OVS RPMs are downloaded from Koji. To build OVN from source instead,
// set ovnGitRef to a branch/tag/commit (e.g., "main"); pass an empty string for Koji.
// cfg.OVNKubernetesPath overrides the source location (empty = auto-clone).
func BuildOVNKubernetesImage(cfg *config.Config, cmdExec platform.CommandExecutor, imageName string, ovnGitRef string) error {
	engine, err := containerengine.NewProjectEngine(cmdExec)
	if err != nil {
		return err
	}
	return BuildOVNKubernetesImageWithEngine(cfg, cmdExec, engine, imageName, ovnGitRef)
}

// BuildOVNKubernetesImageWithEngine builds the image using a preselected
// container engine so callers can detect once and reuse it.
// cfg.OVNKubernetesPath overrides the source location (empty = auto-clone).
func BuildOVNKubernetesImageWithEngine(
	cfg *config.Config,
	cmdExec platform.CommandExecutor,
	engine containerengine.Engine,
	imageName string,
	ovnGitRef string,
) error {
	ctx := context.Background()
	ovnPath, err := EnsureOVNKubernetesSource(cmdExec, cfg.OVNKubernetesPath)
	if err != nil {
		return fmt.Errorf("failed to ensure OVN-Kubernetes source: %w", err)
	}

	dockerfile := filepath.Join(ovnPath, "dist", "images", "Dockerfile.fedora")
	exists, err := cmdExec.FileExists(dockerfile)
	if err != nil {
		return fmt.Errorf("failed to check for Dockerfile.fedora: %w", err)
	}
	if !exists {
		return fmt.Errorf("dockerfile.fedora not found at %s", dockerfile)
	}
	dockerfileContent, err := cmdExec.ReadFile(dockerfile)
	if err != nil {
		return fmt.Errorf("failed to read Dockerfile.fedora: %w", err)
	}

	// Write a .dockerignore to the build context to reduce its size and improve
	// layer cache stability. The COPY in the Dockerfile sends all files from the
	// build context; without this, docs/, test/, .github/, helm/, etc. are
	// included unnecessarily. Any change in those directories would also
	// invalidate the COPY layer cache and trigger a full Go rebuild.
	restoreDockerignore, err := writeDockerignore(cmdExec, ovnPath)
	if err != nil {
		log.Warn("Warning: failed to write .dockerignore, build may be slower: %v", err)
	}
	defer restoreDockerignore()

	// Detect architecture from the executor's target system.
	targetArch, err := cmdExec.GetArchitecture()
	if err != nil {
		return fmt.Errorf("failed to detect architecture: %w", err)
	}
	arch := targetArch.GoArch()
	isPodman := engine.Name() == containerengine.EnginePodman

	// Build the Go builder image reference.
	const goVersion = "1.24"
	goImage := fmt.Sprintf("quay.io/projectquay/golang:%s", goVersion)

	// Determine OVN_FROM: "koji" (pre-built RPMs) or "source" (build from git).
	ovnFrom := "koji"
	// When building OVN from source, resolve the git ref to a SHA and pass it.
	if ovnGitRef != "" {
		ovnFrom = "source"
	}

	resolvedOVNGitRef := ""
	buildOpts := containerengine.BuildOptions{
		ContextDir: ovnPath,
		Dockerfile: dockerfile,
		Platform:   "linux/" + arch,
		BuildArgs: map[string]string{
			"BUILDER_IMAGE":      goImage,
			"OVN_FROM":           ovnFrom,
			"OVN_KUBERNETES_DIR": ".",
			// Podman does not auto-populate BUILDPLATFORM/TARGETOS/TARGETARCH
			// the way Docker BuildKit does. Pass them explicitly so the
			// Dockerfile's cross-compilation logic works correctly.
			"BUILDPLATFORM": "linux/" + arch,
			"TARGETOS":      "linux",
			"TARGETARCH":    arch,
		},
	}

	// When using podman, mount a persistent host directory for the Go build cache.
	// podman build --volume requires absolute host paths (no named volumes).
	// Without this, every COPY layer change wipes the Go compiler cache and
	// forces a full recompilation of all ~1000 packages (~5+ min).
	// With the cache, only changed packages are recompiled.
	// The culprit is the _output/ directory inside go-controller/ which linger in the
	// tree and have different timestamps & content each time. Hence the need for a
	// persistent Go build cache inside the build container.
	if isPodman {
		cacheDir, err := getGoBuildCacheDir()
		if err != nil {
			log.Warn("Warning: could not create Go build cache dir, build may be slower: %v", err)
		} else {
			buildOpts.ExtraArgs = append(buildOpts.ExtraArgs,
				"--volume", cacheDir+":/root/.cache/go-build:Z",
				"--volume", cacheDir+":/go/pkg/mod:Z",
			)
		}
	}

	if ovnGitRef != "" {
		ovnRepo := "https://github.com/ovn-org/ovn.git"
		sha, err := resolveGitRef(cmdExec, ovnRepo, ovnGitRef)
		if err != nil {
			return fmt.Errorf("failed to resolve OVN git ref %q: %w", ovnGitRef, err)
		}
		resolvedOVNGitRef = sha
		buildOpts.BuildArgs["OVN_REPO"] = ovnRepo
		buildOpts.BuildArgs["OVN_GITREF"] = sha
	}

	// Build a deterministic cache key from source/config inputs so any relevant
	// OVN-Kubernetes or build configuration change triggers a new image tag.
	sourceRev, err := ovnKubernetesSourceRevision(cmdExec, ovnPath)
	if err != nil {
		log.Warn("Warning: could not determine OVN-Kubernetes source revision, falling back to non-cached tag: %v", err)
		sourceRev = "unknown"
	}
	cacheKey := ovnKubeImageCacheKey(sourceRev, arch, ovnFrom, resolvedOVNGitRef, dockerfileContent)
	cachedImageName := ovnKubeCachedImageName(imageName, cacheKey)

	// Fast path: reuse existing cached image and retag to requested image name for
	// backward compatibility with downstream registry/manifests flow.
	if engine.ImageExists(ctx, cachedImageName) {
		log.Info("Using cached OVN-Kubernetes image %s", cachedImageName)
		if cachedImageName != imageName {
			if err := engine.Tag(ctx, cachedImageName, imageName); err != nil {
				return fmt.Errorf("failed to tag cached image %s as %s: %w", cachedImageName, imageName, err)
			}
		}
		return nil
	}

	buildOpts.Image = cachedImageName
	log.Info("Building OVN-Kubernetes image %s (cache=%s, OVN_FROM=%s, arch=%s)...", imageName, cacheKey, ovnFrom, arch)
	if err := engine.Build(ctx, buildOpts); err != nil {
		return fmt.Errorf("failed to build OVN-Kubernetes image: %w", err)
	}

	if cachedImageName != imageName {
		if err := engine.Tag(ctx, cachedImageName, imageName); err != nil {
			return fmt.Errorf("failed to tag built image %s as %s: %w", cachedImageName, imageName, err)
		}
	}

	log.Info("✓ OVN-Kubernetes image built successfully: %s (cached as %s)", imageName, cachedImageName)
	return nil
}

// ovnKubernetesSourceRevision returns a source fingerprint based on HEAD, and
// when dirty, appends a hash of tracked-file diffs so local changes invalidate cache.
func ovnKubernetesSourceRevision(cmdExec platform.CommandExecutor, ovnPath string) (string, error) {
	stdout, _, err := cmdExec.Execute(fmt.Sprintf("git -C %q rev-parse HEAD", ovnPath))
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(stdout)
	if head == "" {
		return "", fmt.Errorf("empty git HEAD")
	}

	stdout, _, err = cmdExec.Execute(fmt.Sprintf("git -C %q status --porcelain --untracked-files=no", ovnPath))
	if err != nil {
		return head, nil
	}
	if strings.TrimSpace(stdout) == "" {
		return head, nil
	}

	diffOut, _, err := cmdExec.Execute(fmt.Sprintf("git -C %q diff --no-ext-diff --binary -- .", ovnPath))
	if err != nil {
		return head + "-dirty", nil
	}
	return head + "-dirty-" + shortSHA256(diffOut, 12), nil
}

// ovnKubeImageCacheKey generates a stable image-cache key from build inputs.
func ovnKubeImageCacheKey(sourceRev, arch, ovnFrom, ovnGitRef string, dockerfileContent []byte) string {
	material := strings.Join([]string{
		sourceRev,
		"arch=" + arch,
		"ovn_from=" + ovnFrom,
		"ovn_gitref=" + ovnGitRef,
		"dockerfile_sha=" + shortSHA256Bytes(dockerfileContent, 16),
	}, "\n")
	return shortSHA256(material, 16)
}

// ovnKubeCachedImageName appends cacheKey to the existing tag component.
func ovnKubeCachedImageName(imageName, cacheKey string) string {
	lastColon := strings.LastIndex(imageName, ":")
	lastSlash := strings.LastIndex(imageName, "/")
	if lastColon > lastSlash {
		repo := imageName[:lastColon]
		tag := imageName[lastColon+1:]
		return fmt.Sprintf("%s:%s-%s", repo, tag, cacheKey)
	}
	return fmt.Sprintf("%s:%s", imageName, cacheKey)
}

func shortSHA256(s string, n int) string {
	sum := sha256.Sum256([]byte(s))
	encoded := hex.EncodeToString(sum[:])
	if n <= 0 || n >= len(encoded) {
		return encoded
	}
	return encoded[:n]
}

func shortSHA256Bytes(b []byte, n int) string {
	sum := sha256.Sum256(b)
	encoded := hex.EncodeToString(sum[:])
	if n <= 0 || n >= len(encoded) {
		return encoded
	}
	return encoded[:n]
}

// getGoBuildCacheDir returns an absolute path to a persistent directory used
// to cache Go build artifacts across podman builds. The directory is created
// under the user's cache directory if it does not already exist.
func getGoBuildCacheDir() (string, error) {
	cacheHome, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user cache dir: %w", err)
	}
	dir := filepath.Join(cacheHome, "dpu-sim", "ovn-go-build-cache")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create cache dir %s: %w", dir, err)
	}
	return dir, nil
}

// writeDockerignore writes a .dockerignore file to the build context directory
// and returns a restore function that undoes the change. If a .dockerignore
// already exists its content is saved and restored on cleanup; otherwise the
// file is removed. The returned function is always safe to call (never nil).
func writeDockerignore(cmdExec platform.CommandExecutor, buildContextDir string) (restore func(), err error) {
	dockerignorePath := filepath.Join(buildContextDir, ".dockerignore")

	// Snapshot any pre-existing file so we can restore it later.
	var hadOriginal bool
	var originalContent []byte
	if existing, readErr := cmdExec.ReadFile(dockerignorePath); readErr == nil {
		hadOriginal = true
		originalContent = existing
	}

	restore = func() {
		if hadOriginal {
			if writeErr := cmdExec.WriteFile(dockerignorePath, originalContent, 0644); writeErr != nil {
				log.Debug("Note: failed to restore original .dockerignore at %s: %v", dockerignorePath, writeErr)
			}
		} else {
			if removeErr := cmdExec.RemoveAll(dockerignorePath); removeErr != nil {
				log.Debug("Note: failed to remove .dockerignore at %s: %v", dockerignorePath, removeErr)
			}
		}
	}

	content := strings.Join([]string{
		"# Auto-generated by dpu-sim to speed up docker builds.",
		"# Excludes files not needed by Dockerfile.fedora so the COPY layer",
		"# cache is only invalidated when actual build inputs change.",
		".git",
		".github",
		"contrib",
		"docs",
		"test",
		"helm",
		"contrib",
		"*.yml",
		"*.txt",
		"*.md",
		"**/*_test.go",
		"",
	}, "\n")

	err = cmdExec.WriteFile(dockerignorePath, []byte(content), 0644)
	return restore, err
}

// resolveGitRef resolves a git ref (branch, tag, or commit) to a full SHA using ls-remote.
func resolveGitRef(cmdExec platform.CommandExecutor, repo, ref string) (string, error) {
	stdout, _, err := cmdExec.Execute(fmt.Sprintf("git ls-remote '%s' '%s'", repo, ref))
	if err != nil {
		return "", fmt.Errorf("git ls-remote failed: %w", err)
	}

	lines := strings.TrimSpace(stdout)
	if lines == "" {
		// The ref might already be a commit SHA; return it as-is
		return ref, nil
	}

	// Take the first line and extract the SHA
	parts := strings.Fields(lines)
	if len(parts) < 1 {
		return ref, nil
	}
	return parts[0], nil
}

// labelNodesForDPUHost labels DPU-host worker nodes with the required
// DPU Host node label.
func (m *CNIManager) labelNodesForDPUHost(clusterName string) error {
	dpuHostNodes := m.config.GetDPUHostNodeNames(clusterName)
	if len(dpuHostNodes) == 0 {
		log.Info("No DPU-host nodes found in cluster %s", clusterName)
		return nil
	}

	log.Info("Labeling DPU-host nodes in cluster %s...", clusterName)
	for _, nodeName := range dpuHostNodes {
		labels := map[string]string{
			config.DPUHostNodeLabelKey: "",
		}
		if err := m.k8sClient.LabelNode(nodeName, labels); err != nil {
			return fmt.Errorf("failed to label DPU-host node %s: %w", nodeName, err)
		}
		log.Debug("✓ Labeled node %s with %s", nodeName, config.DPUHostNodeLabelKey)
	}

	log.Info("✓ DPU-host nodes labeled in cluster %s", clusterName)
	return nil
}

// labelNodesForDPU labels DPU nodes with the required k8s.ovn.org/dpu label.
func (m *CNIManager) labelNodesForDPU(clusterName string) error {
	dpuNodes := m.config.GetDPUNodeNames(clusterName)
	if len(dpuNodes) == 0 {
		log.Info("No DPU nodes found in cluster %s", clusterName)
		return nil
	}

	log.Info("Labeling DPU nodes in cluster %s...", clusterName)
	for _, nodeName := range dpuNodes {
		labels := map[string]string{
			"k8s.ovn.org/dpu": "",
		}
		if err := m.k8sClient.LabelNode(nodeName, labels); err != nil {
			return fmt.Errorf("failed to label DPU node %s: %w", nodeName, err)
		}
		log.Debug("✓ Labeled node %s with k8s.ovn.org/dpu", nodeName)
	}

	log.Info("✓ DPU nodes labeled in cluster %s", clusterName)
	return nil
}

// createDPUAccessSecret creates a service account token Secret in the
// ovn-kubernetes namespace for the ovnkube-node service account. The DPU
// cluster uses the token and CA cert from this secret for cross-cluster access.
func (m *CNIManager) createDPUAccessSecret() error {
	log.Info("Creating DPU access secret for cross-cluster authentication...")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ovnkube-node-sa-for-dpu",
			Namespace: "ovn-kubernetes",
			Annotations: map[string]string{
				"kubernetes.io/service-account.name": "ovnkube-node",
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}

	_, err := m.k8sClient.Clientset().CoreV1().Secrets("ovn-kubernetes").Create(ctx, secret, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create DPU access secret: %w", err)
	}

	// Wait for the token controller to populate the secret
	log.Info("Waiting for DPU access secret to be populated...")
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)
		populated, getErr := m.k8sClient.Clientset().CoreV1().Secrets("ovn-kubernetes").Get(ctx, "ovnkube-node-sa-for-dpu", metav1.GetOptions{})
		if getErr != nil {
			continue
		}
		if _, hasToken := populated.Data["token"]; hasToken {
			log.Info("✓ DPU access secret created and populated")
			return nil
		}
	}

	return fmt.Errorf("timed out waiting for DPU access secret to be populated")
}

// CreateOVNKubernetesDPUAccessSecret provisions the host-cluster service
// account token used by OVN-Kubernetes pods running on the DPU cluster.
func (m *CNIManager) CreateOVNKubernetesDPUAccessSecret() error {
	return m.createDPUAccessSecret()
}

// getDPUHostClusterCredentials retrieves the service account token and CA
// certificate from the host cluster's "ovnkube-node-sa-for-dpu" secret,
// along with the API server URL and cluster network information. The host
// cluster kubeconfig path is derived from the config.
func (m *CNIManager) getDPUHostClusterCredentials() (*DPUHostCredentials, error) {
	hostClusterName := m.config.GetDPUHostClusterName()
	hostKubeconfigPath := k8s.GetKubeconfigPath(hostClusterName, m.config.Kubernetes.GetKubeconfigDir())
	log.Info("Retrieving DPU host cluster credentials from %s...", hostKubeconfigPath)

	hostClient, err := k8s.NewClientFromFile(hostKubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create host cluster client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	secret, err := hostClient.Clientset().CoreV1().Secrets("ovn-kubernetes").Get(ctx, "ovnkube-node-sa-for-dpu", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get DPU access secret from host cluster: %w", err)
	}

	tokenBytes, ok := secret.Data["token"]
	if !ok {
		return nil, fmt.Errorf("token not found in DPU access secret")
	}

	caCertBytes, ok := secret.Data["ca.crt"]
	if !ok {
		return nil, fmt.Errorf("ca.crt not found in DPU access secret")
	}

	hostClusterCfg := m.config.GetClusterConfig(hostClusterName)
	if hostClusterCfg == nil {
		return nil, fmt.Errorf("host cluster %s not found in config", hostClusterName)
	}

	// Determine the host cluster API server URL.
	// In Kind mode the kubeconfig server URL points at 127.0.0.1:<random>,
	// which is unreachable from DPU Kind containers. Resolve the control
	// plane container's IP on the "kind" Docker network instead.
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

	credentials := &DPUHostCredentials{
		APIServer:   apiServerURL,
		Token:       string(tokenBytes),
		CACert:      base64.StdEncoding.EncodeToString(caCertBytes),
		PodCIDR:     podCIDR,
		ServiceCIDR: hostClusterCfg.ServiceCIDR,
	}

	log.Info("✓ DPU host cluster credentials retrieved (API: %s, PodCIDR: %s, ServiceCIDR: %s)",
		credentials.APIServer, credentials.PodCIDR, credentials.ServiceCIDR)
	return credentials, nil
}

func kindControlPlaneAPIURL(clusterName string) string {
	return "https://" + clusterName + "-control-plane:6443"
}

// SetupOVNKOffloadToDPUNodeOVS configures OVS external_ids on a DPU node so that
// ovnkube-node in DPU mode can find the encap IP and the paired host node
// name. his must be called after OVS is installed/running
// on the DPU and before helm install.
func SetupOVNKOffloadToDPUNodeOVS(cmdExec platform.CommandExecutor, dpuNodeName, hostNodeName, encapIP string) error {
	log.Info("Configuring OVS external_ids on DPU %s (encap-ip=%s, host=%s)...", dpuNodeName, encapIP, hostNodeName)

	ovsCmd := fmt.Sprintf(
		"ovs-vsctl set Open_vSwitch . "+
			"external_ids:ovn-encap-ip=%s "+
			"external_ids:hostname=%s "+
			"external_ids:host-k8s-nodename=%s "+
			"external_ids:ovn-encap-type=geneve",
		encapIP, dpuNodeName, hostNodeName,
	)

	stdout, stderr, err := cmdExec.ExecuteWithTimeout(ovsCmd, 30*time.Second)
	if err != nil {
		return fmt.Errorf("failed to set OVS external_ids on %s: %w\nstdout: %s\nstderr: %s", dpuNodeName, err, stdout, stderr)
	}

	log.Info("✓ OVS external_ids configured on DPU %s", dpuNodeName)
	return nil
}

// Deprecated: daemonset.sh-based deployment
//
// The functions below implement the legacy daemonset.sh manifest-generation
// approach. They are retained as reference code for anyone who needs to
// understand or restore the old deployment flow. The primary path now uses
// Helm charts (see installOVNKubernetes / redeployOVNKubernetes above).
// ---------------------------------------------------------------------------

// installOVNKubernetesDaemonset installs OVN-Kubernetes CNI by running
// daemonset.sh to generate YAML manifests and applying them directly.
//
// Deprecated: kept as reference. Use installOVNKubernetes (Helm) instead.
func (m *CNIManager) installOVNKubernetesDaemonset(clusterName string, k8sIP string, gatewayInterface string) error { //nolint:unused
	clusterCfg := m.config.GetClusterConfig(clusterName)
	if clusterCfg == nil {
		return fmt.Errorf("cluster configuration not found for cluster %s", clusterName)
	}
	podCIDR := clusterCfg.PodCIDR
	serviceCIDR := clusterCfg.ServiceCIDR
	apiServerURL := "https://" + k8sIP + ":6443"

	log.Info("For OVN-Kubernetes installation, using Pod CIDR: %s, Service CIDR: %s, API Server URL: %s", podCIDR, serviceCIDR, apiServerURL)

	if err := m.patchCoreDNS("8.8.8.8"); err != nil {
		return fmt.Errorf("failed to patch CoreDNS: %w", err)
	}

	ovnKPath, err := EnsureOVNKubernetesSource(m.cmdExec, m.config.OVNKubernetesPath)
	if err != nil {
		return fmt.Errorf("failed to ensure OVN-Kubernetes source: %w", err)
	}

	if err := BuildOVNKubernetesImage(m.config, m.cmdExec, DefaultOVNKubeImage, ""); err != nil {
		return fmt.Errorf("failed to build OVN-Kubernetes image: %w", err)
	}

	regContainer := m.config.GetRegistryContainerForCNI(config.CNIOVNKubernetes)
	ovnImage := m.config.OvnKubernetesImageForHelm(DefaultOVNImage)
	if regContainer != nil {
		log.Info("OVN-Kubernetes daemonset image: %s", ovnImage)
	}

	if err := m.runDaemonsetScript(ovnKPath, apiServerURL, podCIDR, serviceCIDR, ovnImage, gatewayInterface); err != nil {
		return fmt.Errorf("failed to run daemonset.sh: %w", err)
	}

	if err := m.applyOVNKubernetesManifests(ovnKPath, clusterName); err != nil {
		return fmt.Errorf("failed to apply OVN-Kubernetes manifests: %w", err)
	}

	if err := m.k8sClient.WaitForPodsReady("ovn-kubernetes", "", 5*time.Minute); err != nil {
		log.Warn("Warning: OVN-Kubernetes pods may not be ready: %v", err)
	} else {
		log.Info("✓ OVN-Kubernetes pods are ready, installed successfully!")
	}

	if err := m.k8sClient.DeleteDaemonSet("kube-system", "kube-proxy"); err != nil {
		return fmt.Errorf("failed to delete kube-proxy DaemonSet: %w", err)
	}

	return nil
}

// runDaemonsetScript runs the OVN-Kubernetes daemonset.sh script to generate manifests.
//
// Deprecated: kept as reference. The Helm path does not use daemonset.sh.
func (m *CNIManager) runDaemonsetScript(ovnkRepoPath, apiServerURL, podCIDR, serviceCIDR, ovnImage, gatewayInterface string) error { //nolint:unused
	daemonsetScript := filepath.Join(ovnkRepoPath, "dist", "images", "daemonset.sh")

	if _, err := os.Stat(daemonsetScript); os.IsNotExist(err) {
		return fmt.Errorf("daemonset.sh script not found at %s", daemonsetScript)
	}

	if err := os.Chmod(daemonsetScript, 0755); err != nil {
		return fmt.Errorf("failed to make daemonset.sh executable: %w", err)
	}

	args := []string{
		fmt.Sprintf("--image=%s", ovnImage),
		fmt.Sprintf("--net-cidr=%s", podCIDR),
		fmt.Sprintf("--svc-cidr=%s", serviceCIDR),
		fmt.Sprintf("--k8s-apiserver=%s", apiServerURL),
		fmt.Sprintf("--gateway-options=--gateway-interface=%s", gatewayInterface),
		"--image-pull-policy=Always",
		"--gateway-mode=shared",
		"--dummy-gateway-bridge=false",
		"--enable-ipsec=false",
		"--hybrid-enabled=false",
		"--disable-snat-multiple-gws=false",
		"--disable-forwarding=false",
		"--ovn-encap-port=",
		"--disable-pkt-mtu-check=false",
		"--ovn-empty-lb-events=false",
		"--multicast-enabled=false",
		"--ovn-master-count=1",
		"--ovn-unprivileged-mode=no",
		"--master-loglevel=5",
		"--node-loglevel=5",
		"--dbchecker-loglevel=5",
		"--ovn-loglevel-northd=-vconsole:info -vfile:info",
		"--ovn-loglevel-nb=-vconsole:info -vfile:info",
		"--ovn-loglevel-sb=-vconsole:info -vfile:info",
		"--ovn-loglevel-controller=-vconsole:info",
		"--ovnkube-libovsdb-client-logfile=",
		"--ovnkube-config-duration-enable=true",
		"--admin-network-policy-enable=true",
		"--egress-ip-enable=true",
		"--egress-ip-healthcheck-port=9107",
		"--egress-firewall-enable=true",
		"--egress-qos-enable=true",
		"--egress-service-enable=true",
		"--v4-join-subnet=100.64.0.0/16",
		"--v6-join-subnet=fd98::/64",
		"--v4-masquerade-subnet=169.254.0.0/17",
		"--v6-masquerade-subnet=fd69::/112",
		"--v4-transit-subnet=100.88.0.0/16",
		"--v6-transit-subnet=fd97::/64",
		"--ex-gw-network-interface=",
		"--multi-network-enable=false",
		"--network-segmentation-enable=false",
		"--preconfigured-udn-addresses-enable=false",
		"--route-advertisements-enable=false",
		"--advertise-default-network=false",
		"--advertised-udn-isolation-mode=strict",
		"--ovnkube-metrics-scale-enable=false",
		"--compact-mode=false",
		"--enable-multi-external-gateway=true",
		"--enable-ovnkube-identity=true",
		"--enable-persistent-ips=true",
		"--network-qos-enable=false",
		"--mtu=1400",
		"--enable-dnsnameresolver=false",
		"--enable-observ=false",
	}

	log.Info("Running daemonset.sh to generate manifests...")
	scriptDir := filepath.Dir(daemonsetScript)
	stdout, stderr, err := platform.RunCommandInDir(m.cmdExec, scriptDir, daemonsetScript, args, 45*time.Minute)
	output := platform.CombinedCmdOutput(stdout, stderr)
	if err != nil {
		return fmt.Errorf("daemonset.sh failed: %w\n%s", err, output)
	}

	log.Info("✓ daemonset.sh completed successfully")
	log.Debug("daemonset.sh output: %s", output)
	return nil
}

// applyOVNKubernetesManifests applies the OVN-Kubernetes manifests generated by daemonset.sh.
//
// Deprecated: kept as reference. The Helm path uses helm install instead.
func (m *CNIManager) applyOVNKubernetesManifests(ovnPath string, clusterName string) error { //nolint:unused
	yamlDir := filepath.Join(ovnPath, "dist", "yaml")

	if _, err := os.Stat(yamlDir); os.IsNotExist(err) {
		return fmt.Errorf("YAML directory not found at %s", yamlDir)
	}

	crdManifests := []string{
		"k8s.ovn.org_egressfirewalls.yaml",
		"k8s.ovn.org_egressips.yaml",
		"k8s.ovn.org_egressqoses.yaml",
		"k8s.ovn.org_egressservices.yaml",
		"k8s.ovn.org_adminpolicybasedexternalroutes.yaml",
		"k8s.ovn.org_networkqoses.yaml",
		"k8s.ovn.org_userdefinednetworks.yaml",
		"k8s.ovn.org_clusteruserdefinednetworks.yaml",
		"k8s.ovn.org_routeadvertisements.yaml",
		"k8s.ovn.org_clusternetworkconnects.yaml",
	}

	externalCRDs := []string{
		"https://raw.githubusercontent.com/kubernetes-sigs/network-policy-api/v0.1.5/config/crd/experimental/policy.networking.k8s.io_adminnetworkpolicies.yaml",
		"https://raw.githubusercontent.com/kubernetes-sigs/network-policy-api/v0.1.5/config/crd/experimental/policy.networking.k8s.io_baselineadminnetworkpolicies.yaml",
	}

	setupManifests := []string{
		"ovn-setup.yaml",
		"rbac-ovnkube-identity.yaml",
		"rbac-ovnkube-cluster-manager.yaml",
		"rbac-ovnkube-master.yaml",
		"rbac-ovnkube-node.yaml",
		"rbac-ovnkube-db.yaml",
	}

	deploymentManifests := []string{
		"ovnkube-identity.yaml",
	}

	deploymentManifests = append(deploymentManifests, "ovnkube-db.yaml")
	deploymentManifests = append(deploymentManifests, "ovnkube-master.yaml")
	deploymentManifests = append(deploymentManifests, "ovnkube-node.yaml")

	log.Info("Applying OVN-Kubernetes CRD manifests...")
	for _, manifest := range crdManifests {
		manifestPath := filepath.Join(yamlDir, manifest)
		content, err := os.ReadFile(manifestPath)
		if err != nil {
			return fmt.Errorf("failed to read manifest %s: %w", manifest, err)
		}
		if err := m.k8sClient.ApplyManifest(content); err != nil {
			return fmt.Errorf("failed to apply manifest %s: %w", manifest, err)
		}
		log.Debug("✓ Applied CRD manifest %s", manifest)
	}

	log.Info("Applying external CRD manifests...")
	for _, url := range externalCRDs {
		if err := m.k8sClient.ApplyManifestFromURL(url); err != nil {
			return fmt.Errorf("failed to apply external CRD from %s: %w", url, err)
		}
	}

	m.k8sClient.InvalidateDiscoveryCache()

	log.Info("Applying OVN-Kubernetes setup manifests...")
	for _, manifest := range setupManifests {
		manifestPath := filepath.Join(yamlDir, manifest)
		content, err := os.ReadFile(manifestPath)
		if err != nil {
			return fmt.Errorf("failed to read manifest %s: %w", manifest, err)
		}
		if err := m.k8sClient.ApplyManifest(content); err != nil {
			return fmt.Errorf("failed to apply manifest %s: %w", manifest, err)
		}
		log.Info("✓ Applied setup manifest %s", manifest)
	}

	if err := m.labelOVNMasterNodes(clusterName); err != nil {
		return fmt.Errorf("failed to label master nodes: %w", err)
	}

	log.Info("Applying OVN-Kubernetes deployment manifests...")
	for _, manifest := range deploymentManifests {
		manifestPath := filepath.Join(yamlDir, manifest)
		content, err := os.ReadFile(manifestPath)
		if err != nil {
			return fmt.Errorf("failed to read manifest %s: %w", manifest, err)
		}
		if err := m.k8sClient.ApplyManifest(content); err != nil {
			return fmt.Errorf("failed to apply manifest %s: %w", manifest, err)
		}
		log.Info("✓ Applied deployment manifest %s", manifest)
	}

	return nil
}

// redeployOVNKubernetesDaemonset re-applies daemonset.sh manifests and deletes
// pods by label to force recreation.
//
// Deprecated: kept as reference. Use redeployOVNKubernetes (Helm) instead.
func (m *CNIManager) redeployOVNKubernetesDaemonset(clusterName string) error { //nolint:unused
	log.Info("Redeploying OVN-Kubernetes on cluster %s...", clusterName)

	ovnKPath, err := EnsureOVNKubernetesSource(m.cmdExec, m.config.OVNKubernetesPath)
	if err != nil {
		return fmt.Errorf("failed to ensure OVN-Kubernetes source: %w", err)
	}

	if err := m.applyOVNKubernetesManifests(ovnKPath, clusterName); err != nil {
		return fmt.Errorf("failed to re-apply OVN-Kubernetes manifests: %w", err)
	}

	ovnPodLabels := []string{"ovnkube-db", "ovnkube-master", "ovnkube-node", "ovnkube-identity"}
	for _, name := range ovnPodLabels {
		if err := m.k8sClient.DeletePodsByLabel("ovn-kubernetes", "name="+name); err != nil {
			log.Warn("Warning: failed to delete pods with label name=%s: %v", name, err)
		}
	}

	if err := m.k8sClient.WaitForPodsReady("ovn-kubernetes", "", 5*time.Minute); err != nil {
		log.Warn("Warning: OVN-Kubernetes pods may not be ready after redeploy: %v", err)
	} else {
		log.Info("✓ OVN-Kubernetes pods are ready after redeploy on cluster %s", clusterName)
	}
	return nil
}
