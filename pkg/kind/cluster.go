package kind

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/cni"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/deviceplugin"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/k8s"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/linux"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
	"go.yaml.in/yaml/v2"
	"sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	"sigs.k8s.io/kind/pkg/cluster"
)

func (m *KindManager) InstallDependencies(cmdExec platform.CommandExecutor) error {
	deps := []platform.Dependency{
		{
			Name:        "IPv6",
			Reason:      "Required for Kind clusters",
			CheckFunc:   linux.CheckIpv6,
			InstallFunc: linux.ConfigureIpv6,
		},
		{
			Name:        "Open vSwitch",
			Reason:      "Required for OVN-Kubernetes",
			CheckCmd:    []string{"ovs-vsctl", "--version"},
			InstallFunc: linux.InstallOpenVSwitchWithoutSystemd,
		},
	}

	if m.needsKindBridgeCNIPlugins() {
		deps = append(deps, platform.Dependency{
			Name:        "CNI plugins",
			Reason:      "Required by flannel and multus on Kind nodes",
			CheckFunc:   linux.CheckKindCNIPlugins,
			InstallFunc: linux.InstallKindCNIPlugins,
		})
	}

	return platform.EnsureDependenciesWithExecutor(cmdExec, deps, m.config)
}

// needsKindBridgeCNIPlugins returns true when Kind nodes need the standard CNI
// bridge plugin set in /opt/cni/bin.
//
// Why: flannel delegates to the CNI bridge/host-local plugins, and multus in
// this project chains to the cluster's primary CNI. If bridge binaries are
// missing, pod sandbox creation fails after multus rollout with errors like:
// "failed to find plugin bridge in path [/opt/cni/bin]".
//
// As a fix we gate plugin installation to clusters that use flannel directly or enable
// multus, so OVN-only Kind runs do not install extra packages unnecessarily.
func (m *KindManager) needsKindBridgeCNIPlugins() bool {
	for _, clusterCfg := range m.config.Kubernetes.Clusters {
		if clusterCfg.CNI == config.CNIFlannel {
			return true
		}
		for _, addon := range clusterCfg.Addons {
			if addon == config.AddonMultus {
				return true
			}
		}
	}
	return false
}

func (m *KindManager) InstallHostDependencies(cmdExec platform.CommandExecutor) error {
	deps := []platform.Dependency{
		{
			Name:        "Inotify Limits",
			Reason:      "Required for OVN-Kubernetes webhook stability on Kind",
			CheckFunc:   linux.CheckInotifyLimits,
			InstallFunc: linux.ConfigureInotifyLimits,
		},
	}

	if m.needsKindBridgeCNIPlugins() {
		deps = append(deps, platform.Dependency{
			Name:        "br_netfilter",
			Reason:      "Required by flannel for bridge-nf-call-iptables inside Kind nodes",
			CheckFunc:   linux.CheckBrNetfilter,
			InstallFunc: linux.ConfigureBrNetfilter,
		})
	}

	return platform.EnsureDependenciesWithExecutor(cmdExec, deps, m.config)
}

// DeployAllClusters deploys all Kind clusters defined in the config
func (m *KindManager) DeployAllClusters() error {
	for _, clusterCfg := range m.config.Kubernetes.Clusters {
		kindCfg, err := m.BuildKindConfig(clusterCfg.Name, clusterCfg)
		if err != nil {
			return fmt.Errorf("failed to build Kind config for %s: %w", clusterCfg.Name, err)
		}

		kubeconfigPath := k8s.GetKubeconfigPath(clusterCfg.Name, m.config.Kubernetes.GetKubeconfigDir())
		if err := m.createCluster(clusterCfg.Name, kindCfg, kubeconfigPath); err != nil {
			return fmt.Errorf("failed to create cluster %s: %w", clusterCfg.Name, err)
		}

		// Rewrite with our verified kubeconfig (guards against podman provider bugs).
		if err := m.GetKubeconfig(clusterCfg.Name, kubeconfigPath); err != nil {
			return fmt.Errorf("failed to save kubeconfig for %s: %w", clusterCfg.Name, err)
		}
	}
	return nil
}

// setupOVNKubernetesOffloadToDPUOVS installs OVS inside each DPU Kind container and
// configures the external_ids that ovnkube-node DPU mode requires. This
// mirrors the VM flow where OVS is pre-installed on the DPU hardware.
func (m *KindManager) setupOVNKubernetesOffloadToDPUOVS(cmdExec platform.CommandExecutor, dpuClusterName string) error {
	pairs := m.config.GetHostDPUPairs(dpuClusterName)
	if len(pairs) == 0 {
		return nil
	}

	for _, pair := range pairs {
		dpuExec := platform.NewDockerExecutor(pair.DPUNode, m.containerBin)

		encapIP, err := m.getContainerNetworkIP(cmdExec, pair.DPUNode, m.config.DPUKindGatewayNetworkName())
		if err != nil {
			return fmt.Errorf("failed to get gateway IP for DPU container %s: %w", pair.DPUNode, err)
		}

		if err := cni.SetupOVNKOffloadToDPUNodeOVS(dpuExec, pair.DPUNode, pair.HostNode, encapIP.String()); err != nil {
			return err
		}
	}

	return nil
}

// getContainerIP returns the IP address of a Kind container on the kind
// Docker network.
func (m *KindManager) getContainerIP(cmdExec platform.CommandExecutor, containerName string) (string, error) {
	ip, err := m.getContainerNetworkIP(cmdExec, containerName, "kind")
	if err != nil {
		return "", err
	}
	return ip.String(), nil
}

func (m *KindManager) getContainerNetworkIP(cmdExec platform.CommandExecutor, containerName, networkName string) (net.IP, error) {
	// CommandExecutor.Execute runs via sh -c; keep the inspect format in single quotes
	// and POSIX-quote the binary and container name (same style as RunCommandInDir).
	shCmd := platform.ShQuote(m.containerBin) +
		fmt.Sprintf(" inspect -f '{{ (index .NetworkSettings.Networks %q).IPAddress }}' ", networkName) +
		platform.ShQuote(containerName)
	stdout, stderr, err := cmdExec.ExecuteWithTimeout(shCmd, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to get IP for container %s on network %s: %w\nstderr: %s", containerName, networkName, err, strings.TrimSpace(stderr))
	}
	ipStr := strings.TrimSpace(stdout)
	if ipStr == "" || ipStr == "<no value>" {
		return nil, fmt.Errorf("IP is empty for container %s on network %s", containerName, networkName)
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil, fmt.Errorf("invalid IP %q for container %s on network %s", ipStr, containerName, networkName)
	}

	return ip, nil
}

// preloadCNIImages pulls the images deployed by the flannel and Multus
// manifests once on the host and loads them into every Kind cluster that uses
// them. Without this, every node of every cluster pulls the images from their
// registries independently (the Multus thick image alone is ~180MB). The
// images are resolved from the manifests themselves, so they cannot drift from
// what installFlannel/installMultus later apply. Neither manifest sets an
// imagePullPolicy or uses the :latest tag, so the kubelet default
// (IfNotPresent) uses the preloaded images. Failures are non-fatal: nodes fall
// back to pulling the images directly.
func (m *KindManager) preloadCNIImages(cmdExec platform.CommandExecutor) {
	manifestClusters := map[string][]string{}
	for _, clusterCfg := range m.config.Kubernetes.Clusters {
		if clusterCfg.CNI == config.CNIFlannel {
			manifestClusters[cni.FlannelManifestURL] = append(manifestClusters[cni.FlannelManifestURL], clusterCfg.Name)
		}
		for _, addon := range clusterCfg.Addons {
			if addon == config.AddonMultus {
				manifestClusters[cni.MultusManifestURL] = append(manifestClusters[cni.MultusManifestURL], clusterCfg.Name)
				break
			}
		}
	}

	for url, clusters := range manifestClusters {
		images, err := cni.ManifestImages(url)
		if err != nil {
			log.Warn("Failed to resolve images of manifest %s; nodes will pull them directly: %v", url, err)
			continue
		}
		for _, image := range images {
			log.Info("Pulling image %s once for clusters %v...", image, clusters)
			if err := cmdExec.RunCmd(log.LevelInfo, m.containerBin, "pull", image); err != nil {
				log.Warn("Failed to pull image %s; nodes will pull it directly: %v", image, err)
				continue
			}
			if err := m.KindLoadImageIntoClusters(cmdExec, image, clusters); err != nil {
				log.Warn("Failed to preload image %s into Kind clusters; nodes will pull it directly: %v", image, err)
			}
		}
	}
}

// InstallCNI installs the CNI on every Kind cluster.
func (m *KindManager) InstallCNI(cmdExec platform.CommandExecutor) error {
	m.preloadCNIImages(cmdExec)

	for _, clusterCfg := range m.config.ClustersOrderedForInstall() {
		log.Info("\n--- Installing CNI on cluster %s ---", clusterCfg.Name)
		cniType := clusterCfg.CNI

		// OVN-K images are needed when the cluster uses OVN-K directly, or
		// when this is the DPU cluster in offload mode (OVN-K DPU mode is
		// deployed automatically alongside the primary CNI).
		needsOVNK := cniType == config.CNIOVNKubernetes ||
			m.config.DPUClusterNeedsOVNK(clusterCfg.Name)

		if needsOVNK {
			regContainer := m.config.GetRegistryContainerForCNI(config.CNIOVNKubernetes)
			switch {
			case regContainer == nil:
				if err := m.PullAndLoadImage(cmdExec, clusterCfg.Name, cni.DefaultOVNImage); err != nil {
					return fmt.Errorf("failed to load OVN-Kubernetes image: %w", err)
				}
				if m.config.IsOffloadDPU() {
					if err := m.PullAndLoadImage(cmdExec, clusterCfg.Name, deviceplugin.DefaultDevicePluginImage); err != nil {
						return fmt.Errorf("failed to load device plugin image: %w", err)
					}
				}
			case m.config.IsRegistryEnabled():
				log.Info("Using local registry image for OVN-Kubernetes (tag: %s)", regContainer.Tag)
			default:
				log.Info("OVN-Kubernetes image was built and loaded into Kind (registry disabled; tag: %s)", regContainer.Tag)
			}
		}

		if m.config.DPUClusterNeedsOVNK(clusterCfg.Name) {
			if err := m.setupOVNKubernetesOffloadToDPUOVS(cmdExec, clusterCfg.Name); err != nil {
				return fmt.Errorf("failed to setup OVS on DPU containers: %w", err)
			}
		}

		kubeconfigPath := k8s.GetKubeconfigPath(clusterCfg.Name, m.config.Kubernetes.GetKubeconfigDir())
		cniMgr, err := cni.NewCNIManagerWithKubeconfigFile(m.config, kubeconfigPath, cmdExec)
		if err != nil {
			return fmt.Errorf("failed to create CNI manager: %w", err)
		}

		apiServerIP, err := m.GetInternalAPIServerIP(cmdExec, clusterCfg.Name)
		if err != nil {
			return fmt.Errorf("failed to get internal API server IP for cluster %s: %w", clusterCfg.Name, err)
		}

		if err := cniMgr.InstallCNIAndAddons(cniType, clusterCfg.Name, apiServerIP, clusterCfg.Addons); err != nil {
			return fmt.Errorf("failed to install CNI on cluster %s: %w", clusterCfg.Name, err)
		}

		if err := cniMgr.PostInstallPerCluster(clusterCfg.Name); err != nil {
			return fmt.Errorf("failed to patch cluster environment on cluster %s: %w", clusterCfg.Name, err)
		}
	}

	if err := cni.PostInstall(m.config, cmdExec); err != nil {
		return fmt.Errorf("failed CNI post-install after all Kind clusters: %w", err)
	}

	log.Info("\n✓ CNI installation complete on Kind clusters")
	return nil
}

// createCluster creates a new Kind cluster using the v1alpha4.Cluster config directly.
// kubeconfigPath tells Kind's internal kubeconfig export where to write. This
// prevents Kind from merging into $KUBECONFIG (which may point to a different
// cluster's file), avoiding cross-cluster kubeconfig contamination.
func (m *KindManager) createCluster(name string, cfg *v1alpha4.Cluster, kubeconfigPath string) error {
	if m.ClusterExists(name) {
		log.Info("Kind cluster %s already exists, skipping creation", name)
		return nil
	}

	log.Info("Creating Kind cluster: %s", name)

	var opts []cluster.CreateOption
	if cfg != nil {
		data, err := yaml.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("failed to marshal Kind config: %w", err)
		}

		log.Debug("Generated Kind config:")
		log.Debug("%s", string(data))

		opts = append(opts, cluster.CreateWithV1Alpha4Config(cfg))
	}

	// Direct Kind's internal kubeconfig export to the per-cluster file.
	opts = append(opts, cluster.CreateWithKubeconfigPath(kubeconfigPath))

	if err := m.provider.Create(name, opts...); err != nil {
		return fmt.Errorf("failed to create Kind cluster %s: %w", name, err)
	}

	if err := m.raiseNodePIDsLimits(platform.NewLocalExecutor(), name); err != nil {
		log.Warn("Failed to raise Kind node pids limit on %s: %v", name, err)
	}

	log.Info("✓ Created Kind cluster: %s", name)
	return nil
}

// raiseNodePIDsLimits removes the default 2048 PID cgroup cap on Kind node
// containers. Dense VF pod churn spawns many concurrent CNI plugin processes;
// without this, ovn-k8s-cni fails with "failed to create new OS thread" / errno=11.
func (m *KindManager) raiseNodePIDsLimits(cmdExec platform.CommandExecutor, clusterName string) error {
	nodes, err := m.provider.ListNodes(clusterName)
	if err != nil {
		return fmt.Errorf("list kind nodes for %s: %w", clusterName, err)
	}

	var updated int
	for _, node := range nodes {
		name := node.String()
		// As per docker & podman docs, --pids-limit=-1 means no limit.
		if err := cmdExec.RunCmd(log.LevelInfo, m.containerBin, "update", "--pids-limit", "-1", name); err != nil {
			return fmt.Errorf("raise pids limit on %s: %w", name, err)
		}
		updated++
	}
	if updated > 0 {
		log.Info("✓ Raised pids limit on %d Kind node(s) in cluster %s", updated, clusterName)
	}
	return nil
}

// DeleteCluster deletes a Kind cluster
func (m *KindManager) DeleteCluster(name string) error {
	if !m.ClusterExists(name) {
		log.Info("Kind cluster %s does not exist, skipping deletion", name)
		return nil
	}

	log.Info("Deleting Kind cluster: %s", name)

	if err := m.provider.Delete(name, ""); err != nil {
		return fmt.Errorf("failed to delete Kind cluster %s: %w", name, err)
	}

	log.Info("✓ Deleted Kind cluster: %s", name)
	return nil
}

// ClusterExists checks if a Kind cluster exists
func (m *KindManager) ClusterExists(name string) bool {
	clusters, err := m.provider.List()
	if err != nil {
		return false
	}

	for _, cluster := range clusters {
		if cluster == name {
			return true
		}
	}
	return false
}

// ListClusters lists all Kind clusters
func (m *KindManager) ListClusters() ([]string, error) {
	clusters, err := m.provider.List()
	if err != nil {
		return nil, fmt.Errorf("failed to list Kind clusters: %w", err)
	}
	return clusters, nil
}

// ConfigureRegistryOnNode writes the containerd host-level registry
// configuration so the node can pull from the insecure local registry.
// Image refs use localhost:<port>, but the actual registry container lives
// on the kind Docker network. registryIP is the container's IP on that
// network (obtained via RegistryManager.GetKindNetworkIP after connecting).
// Must be called after cluster creation (the containerd config_path patch
// is applied at creation time via BuildKindConfig).
func (m *KindManager) ConfigureRegistryOnNode(cmdExec platform.CommandExecutor, registryIP string) error {
	imageEndpoint := m.config.GetRegistryNodeEndpoint()
	registryAddr := fmt.Sprintf("%s:%s", registryIP, m.config.GetRegistryPort())
	hostsDir := fmt.Sprintf("/etc/containerd/certs.d/%s", imageEndpoint)

	if err := cmdExec.RunCmd(log.LevelDebug, "mkdir", "-p", hostsDir); err != nil {
		return fmt.Errorf("failed to create containerd certs.d directory: %w", err)
	}

	hostsTOML := fmt.Sprintf("server = \"http://%s\"\n\n[host.\"http://%s\"]\n  capabilities = [\"pull\", \"resolve\"]\n",
		registryAddr, registryAddr)

	if err := cmdExec.WriteFile(hostsDir+"/hosts.toml", []byte(hostsTOML), 0o644); err != nil {
		return fmt.Errorf("failed to write hosts.toml: %w", err)
	}

	return nil
}

// GetClusterInfo retrieves information about a Kind cluster
func (m *KindManager) GetClusterInfo(cmdExec platform.CommandExecutor, name string) (*ClusterInfo, error) {
	if !m.ClusterExists(name) {
		return nil, fmt.Errorf("cluster %s does not exist", name)
	}

	info := &ClusterInfo{
		Name:   name,
		Status: "running",
		Nodes:  []NodeInfo{},
	}

	// Get nodes using the kind provider
	nodes, err := m.provider.ListNodes(name)
	if err != nil {
		return info, nil // Return partial info on error
	}

	for _, node := range nodes {
		nodeName := node.String()
		role := "worker"
		if strings.Contains(nodeName, "control-plane") {
			role = "control-plane"
		}

		// Check node status using kubectl (still needed for detailed status)
		status := "Unknown"
		ctxName := fmt.Sprintf("kind-%s", name)
		// Execute runs via sh -c; double-quote jsonpath so single quotes inside the filter stay literal.
		shCmd := fmt.Sprintf(
			`kubectl get node %q --context %q -o "jsonpath={.status.conditions[?(@.type=='Ready')].status}"`,
			nodeName, ctxName,
		)
		output, _, err := cmdExec.ExecuteWithTimeout(shCmd, 30*time.Second)
		if err == nil {
			if strings.TrimSpace(output) == "True" {
				status = "Ready"
			} else {
				status = "NotReady"
			}
		}

		info.Nodes = append(info.Nodes, NodeInfo{
			Name:   nodeName,
			Role:   role,
			Status: status,
		})
	}

	return info, nil
}

// controlPlaneContainer returns the expected container name for a Kind
// cluster's control plane node.
func controlPlaneContainer(clusterName string) string {
	return clusterName + "-control-plane"
}

// GetKubeconfig retrieves the kubeconfig for a Kind cluster and writes it to disk.
func (m *KindManager) GetKubeconfig(name string, kubeconfigPath string) error {
	if !m.ClusterExists(name) {
		return fmt.Errorf("cluster %s does not exist", name)
	}

	kubeconfig, err := m.provider.KubeConfig(name, false)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig for cluster %s: %w", name, err)
	}

	kubeconfigDir := filepath.Dir(kubeconfigPath)
	if err := os.MkdirAll(kubeconfigDir, 0o755); err != nil {
		return fmt.Errorf("failed to create kubeconfig directory %s: %w", kubeconfigDir, err)
	}

	if err := os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0o600); err != nil {
		return fmt.Errorf("failed to write kubeconfig to %s: %w", kubeconfigPath, err)
	}

	log.Info("✓ Kubeconfig saved to: %s", kubeconfigPath)
	return nil
}

func (m *KindManager) GetKubeconfigContent(name string) (string, error) {
	if !m.ClusterExists(name) {
		return "", fmt.Errorf("cluster %s does not exist", name)
	}

	return m.provider.KubeConfig(name, false)
}

// kindExperimentalProviderEnv returns extra environment for external `kind` subprocesses
// when the node runtime is Podman. The in-process cluster.Provider may use Podman
// (ProviderWithPodman); the kind CLI defaults to Docker unless KIND_EXPERIMENTAL_PROVIDER
// is set, which breaks load/list against Podman-backed clusters (e.g. "no nodes found for cluster ...").
func (m *KindManager) kindExperimentalProviderEnv() []string {
	if m.containerBin == "podman" {
		return []string{"KIND_EXPERIMENTAL_PROVIDER=podman"}
	}
	return nil
}

// kindImageArchiveTempDir returns the directory for temporary image archive files
// used by KindLoadImage. Override /tmp with DPU_SIM_KIND_IMAGE_TMPDIR
func kindImageArchiveTempDir() string {
	return os.Getenv("DPU_SIM_KIND_IMAGE_TMPDIR")
}

// saveImageArchive exports imageName from the local container runtime to a temporary tar.
// The caller must invoke the returned cleanup function when the archive is no longer needed.
func (m *KindManager) saveImageArchive(cmdExec platform.CommandExecutor, imageName string) (archivePath string, cleanup func(), err error) {
	tmpArchive, err := os.CreateTemp(kindImageArchiveTempDir(), "dpu-sim-kind-image-*.tar")
	if err != nil {
		return "", nil, fmt.Errorf("create temp file for kind image load: %w", err)
	}
	archivePath = tmpArchive.Name()
	cleanup = func() { _ = os.Remove(archivePath) }
	if err := tmpArchive.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close temp file for kind image load failed: %w", err)
	}

	log.Info("Saving image %s to archive (via %s save)...", imageName, m.containerBin)

	// With Docker's containerd image store (default since Docker 27), saving a
	// pulled multi-arch image produces an archive whose OCI index references
	// every platform but only contains the host platform's blobs. Kind's
	// "ctr images import --all-platforms" then fails with "content digest ...
	// not found". Restrict the archive to the host platform (docker save
	// --platform, Docker 28+). Podman archives are single-platform already.
	if m.containerBin == "docker" {
		hostPlatform := "linux/" + runtime.GOARCH
		err := cmdExec.RunCmd(log.LevelInfo, m.containerBin,
			"save", "--platform", hostPlatform, "-o", archivePath, imageName)
		if err == nil {
			return archivePath, cleanup, nil
		}
		// Older Docker (< 28) rejects --platform; single-platform images may
		// also lack the requested platform entry. Fall through to a plain save.
		log.Warn("docker save --platform %s failed for %s; retrying without platform filter: %v",
			hostPlatform, imageName, err)
	}

	if err := cmdExec.RunCmd(log.LevelInfo, m.containerBin, "save", "-o", archivePath, imageName); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to save image %q with %s: %w", imageName, m.containerBin, err)
	}

	return archivePath, cleanup, nil
}

// loadImageArchiveIntoCluster loads the image archive into a Kind cluster.
func (m *KindManager) loadImageArchiveIntoCluster(cmdExec platform.CommandExecutor, clusterName, archivePath, imageName string) error {
	if !m.ClusterExists(clusterName) {
		return fmt.Errorf("cluster %s does not exist", clusterName)
	}

	log.Info("Loading image %s into cluster %s (via kind load image-archive)...", imageName, clusterName)
	if err := cmdExec.RunCmdWithExtraEnv(log.LevelInfo, m.kindExperimentalProviderEnv(), "kind", "load", "image-archive", archivePath, "--name", clusterName); err != nil {
		return fmt.Errorf("failed to kind load image archive for %s: %w", imageName, err)
	}

	log.Info("✓ Loaded image %s into cluster %s", imageName, clusterName)
	return nil
}

// KindLoadImage loads a container image from the local runtime (same engine Kind uses:
// docker or podman) into a single Kind cluster.
//
// We use "kind load image-archive" after a local "docker/podman save" instead of
// "kind load docker-image" because the latter only consults the Docker daemon's
// classic image list. That breaks when images live only in the containerd image
// store (Docker 27+ default; see kubernetes-sigs/kind#3795) or when images were
// built with Podman while Kind is configured for the podman provider.
func (m *KindManager) KindLoadImage(cmdExec platform.CommandExecutor, clusterName, imageName string) error {
	return m.KindLoadImageIntoClusters(cmdExec, imageName, []string{clusterName})
}

// KindLoadImageIntoClusters loads a container image from the local runtime (same engine Kind uses:
// docker or podman) into a multiple Kind clusters.
//
// There is an optimaization that saves imageName once and loads the archive into each cluster.
// This avoids duplicating large tar exports when the same image is needed on multiple Kind clusters.
func (m *KindManager) KindLoadImageIntoClusters(cmdExec platform.CommandExecutor, imageName string, clusterNames []string) error {
	if len(clusterNames) == 0 {
		return nil
	}

	archivePath, cleanup, err := m.saveImageArchive(cmdExec, imageName)
	if err != nil {
		return err
	}
	defer cleanup()

	for _, clusterName := range clusterNames {
		if err := m.loadImageArchiveIntoCluster(cmdExec, clusterName, archivePath, imageName); err != nil {
			return fmt.Errorf("kind load %q into cluster %q: %w", imageName, clusterName, err)
		}
	}

	return nil
}

// ExportLogs exports logs from a Kind cluster
func (m *KindManager) ExportLogs(clusterName, outputDir string) error {
	if !m.ClusterExists(clusterName) {
		return fmt.Errorf("cluster %s does not exist", clusterName)
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	log.Info("Exporting logs from cluster %s to %s...", clusterName, outputDir)

	if err := m.provider.CollectLogs(clusterName, outputDir); err != nil {
		return fmt.Errorf("failed to export logs: %w", err)
	}

	log.Info("✓ Logs exported to: %s", outputDir)
	return nil
}

// GetInternalAPIServerIP retrieves the internal API server IP for a Kind cluster.
// This returns the control plane node's IP address on the kind container network,
// suitable for in-cluster communication (e.g., https://172.18.0.2:6443).
func (m *KindManager) GetInternalAPIServerIP(cmdExec platform.CommandExecutor, clusterName string) (string, error) {
	if !m.ClusterExists(clusterName) {
		return "", fmt.Errorf("cluster %s does not exist", clusterName)
	}

	nodeIP, err := m.getContainerIP(cmdExec, controlPlaneContainer(clusterName))
	if err != nil {
		return "", err
	}

	log.Info("Internal API server IP for cluster %s: %s", clusterName, nodeIP)
	return nodeIP, nil
}

// PullAndLoadImage pulls a container image from a registry and loads it into a Kind cluster.
func (m *KindManager) PullAndLoadImage(cmdExec platform.CommandExecutor, clusterName, imageName string) error {
	if !m.ClusterExists(clusterName) {
		return fmt.Errorf("cluster %s does not exist", clusterName)
	}

	log.Info("Pulling image %s...", imageName)

	if err := cmdExec.RunCmd(log.LevelInfo, m.containerBin, "pull", imageName); err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}

	log.Info("✓ Pulled image: %s", imageName)

	return m.KindLoadImage(cmdExec, clusterName, imageName)
}
