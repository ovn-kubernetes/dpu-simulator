package deviceplugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/lib/dpusim"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/containerengine"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/k8s"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/registry"
)

const (
	DevicePluginImage         = "dpu-sim-dp:latest"
	DefaultDevicePluginImage  = "quay.io/wizhao/dpu-sim-dp:latest"
	DevicePluginDaemonSetName = "dpu-sim-device-plugin"
	DevicePluginNamespace     = "kube-system"

	MgmtVFResourceName = "dpusim.io/mgmtvf"
	// VFResourceName is the extended-resource name for simulated VFs.
	VFResourceName = "dpusim.io/vf"

	// MgmtPortVFsCountEnvVar is injected into the device-plugin DaemonSet from
	// the simulator config networks[].mgmt_port_vfs_count value.
	MgmtPortVFsCountEnvVar = "MGMT_PORT_VFS_COUNT"
)

// ResourcePool describes one class of simulated device resources.
// Each pool gets its own gRPC socket, kubelet registration, and env var.
type ResourcePool struct {
	// ResourceName is the extended-resource name advertised to kubelet
	// (e.g. "dpusim.io/mgmtvf").
	ResourceName string

	// SocketName is the filename of the Unix socket under
	// /var/lib/kubelet/device-plugins/ (must be unique per pool).
	SocketName string

	// EnvVarName is the environment variable injected into containers on
	// allocation (mirrors the PCIDEVICE_* convention from SR-IOV Device Plugin).
	EnvVarName string

	// matchesIface selects which host interfaces belong to this pool.
	matchesIface func(name string) bool

	// hostIfIndexStart and hostIfIndexEnd describe the eth0-* indices matched by
	// matchesIface. hostIfIndexEnd is zero when there is no upper bound.
	hostDataIfIndexStart int
	hostDataIfIndexEnd   int
}

// MatchesIface reports whether ifaceName belongs to this resource pool.
func (p ResourcePool) MatchesIface(ifaceName string) bool {
	return p.matchesIface(ifaceName)
}

// MatcherDescription returns a human-readable summary of the pool selector.
func (p ResourcePool) MatcherDescription() string {
	if p.hostDataIfIndexEnd > 0 {
		if p.hostDataIfIndexStart == p.hostDataIfIndexEnd {
			return dpusim.HostDataIf(p.hostDataIfIndexStart)
		}
		return fmt.Sprintf("%s..%s", dpusim.HostDataIf(p.hostDataIfIndexStart), dpusim.HostDataIf(p.hostDataIfIndexEnd))
	}
	if p.hostDataIfIndexStart > 0 {
		return fmt.Sprintf("%s..", dpusim.HostDataIf(p.hostDataIfIndexStart))
	}
	return "custom"
}

// BuildResourcePools returns mgmt and pod VF pools for the given management-port
// VF count. Mgmt VFs are eth0-1 through eth0-N; pod VFs start at eth0-(N+1).
// eth0-0 is reserved for the DPU gateway interface and is excluded from both pools.
func BuildResourcePools(mgmtPortVFsCount int) []ResourcePool {
	if mgmtPortVFsCount < 1 {
		mgmtPortVFsCount = config.DefaultMgmtPortVFsCount
	}
	podVFStart := mgmtPortVFsCount + 1

	return []ResourcePool{
		{
			ResourceName:         MgmtVFResourceName,
			SocketName:           "dpusim-mgmtvf.sock",
			EnvVarName:           "PCIDEVICE_DPUSIM_IO_MGMTVF",
			matchesIface:         hostDataIfInRange(1, mgmtPortVFsCount),
			hostDataIfIndexStart: 1,
			hostDataIfIndexEnd:   mgmtPortVFsCount,
		},
		{
			ResourceName:         VFResourceName,
			SocketName:           "dpusim-vf.sock",
			EnvVarName:           "PCIDEVICE_DPUSIM_IO_VF",
			matchesIface:         hostDataIfAtLeast(podVFStart),
			hostDataIfIndexStart: podVFStart,
		},
	}
}

// hostDataIfIndex parses a host-to-DPU data interface name (eth0-<index>) and
// returns the trailing VF index. Non-host names and malformed values return false.
func hostDataIfIndex(name string) (int, bool) {
	if !strings.HasPrefix(name, "eth0-") {
		return 0, false
	}
	m := dpusim.ReSimulationNetdevFunc.FindStringSubmatch(name)
	if len(m) != 3 || m[1] != "0" {
		return 0, false
	}
	idx, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, false
	}
	return idx, true
}

// hostDataIfInRange returns a matcher for host data interfaces whose index is
// in the closed interval [low, high] (e.g. eth0-1 through eth0-3 when low=1, high=3).
func hostDataIfInRange(low, high int) func(string) bool {
	return func(name string) bool {
		idx, ok := hostDataIfIndex(name)
		return ok && idx >= low && idx <= high
	}
}

// hostDataIfAtLeast returns a matcher for host data interfaces whose index is
// greater than or equal to start (e.g. eth0-4 and above when start=4).
func hostDataIfAtLeast(start int) func(string) bool {
	return func(name string) bool {
		idx, ok := hostDataIfIndex(name)
		return ok && idx >= start
	}
}

// MgmtPortVFsCountFromEnv reads MGMT_PORT_VFS_COUNT from the environment.
// When unset or invalid it returns config.DefaultMgmtPortVFsCount.
func MgmtPortVFsCountFromEnv() int {
	raw := strings.TrimSpace(os.Getenv(MgmtPortVFsCountEnvVar))
	if raw == "" {
		return config.DefaultMgmtPortVFsCount
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 1 {
		return config.DefaultMgmtPortVFsCount
	}
	return count
}

// BuildAndLoadImage builds the device plugin image and pushes it to the
// provided image loader (e.g. a local registry). Returns the image reference
// that Kubernetes manifests should use.
func BuildAndLoadImage(cmdExec platform.CommandExecutor, engine containerengine.Engine, loader registry.ImageLoader) (string, error) {
	if err := BuildDevicePluginImage(cmdExec, engine); err != nil {
		return "", err
	}
	return loader.LoadImage(DevicePluginImage, DevicePluginImage)
}

// BuildDevicePluginImage builds the dpu-sim device plugin container image
// from the device plugin Dockerfile.
func BuildDevicePluginImage(cmdExec platform.CommandExecutor, engine containerengine.Engine) error {
	projectRoot, err := platform.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("failed to get project root: %w", err)
	}

	dockerfile := filepath.Join(projectRoot, "deploy", "device-plugin", "Dockerfile")
	exists, err := cmdExec.FileExists(dockerfile)
	if err != nil {
		return fmt.Errorf("failed to check Dockerfile: %w", err)
	}
	if !exists {
		return fmt.Errorf("device plugin Dockerfile not found at %s", dockerfile)
	}

	targetArch, err := cmdExec.GetArchitecture()
	if err != nil {
		return fmt.Errorf("failed to detect architecture: %w", err)
	}

	buildOpts := containerengine.BuildOptions{
		ContextDir: projectRoot,
		Dockerfile: dockerfile,
		Image:      DevicePluginImage,
		Platform:   "linux/" + targetArch.GoArch(),
	}

	log.Info("Building Device Plugin image %s (Architecture=%s)...", DevicePluginImage, targetArch)
	if err := engine.Build(context.Background(), buildOpts); err != nil {
		return fmt.Errorf("failed to build Device Plugin image: %w", err)
	}

	log.Info("✓ Device Plugin image built: %s", DevicePluginImage)
	return nil
}

// deployDevicePlugin deploys the simulated device plugin DaemonSet onto the
// current cluster. The manifest template is read from deploy/device-plugin/
// and the image placeholder is replaced with the actual image reference.
func DeployDevicePlugin(k8sClient *k8s.K8sClient, imageRef string, mgmtPortVFsCount int) error {
	projectRoot, err := platform.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("failed to get project root: %w", err)
	}

	manifestPath := filepath.Join(projectRoot, "deploy", "device-plugin", "daemonset.yaml")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to read device plugin DaemonSet manifest: %w", err)
	}

	if mgmtPortVFsCount < 1 {
		mgmtPortVFsCount = config.DefaultMgmtPortVFsCount
	}

	manifest := string(manifestBytes)
	manifest = strings.ReplaceAll(manifest, "DPU_SIM_DP_IMAGE", imageRef)
	manifest = strings.ReplaceAll(manifest, "DPU_SIM_MGMT_PORT_VFS_COUNT", strconv.Itoa(mgmtPortVFsCount))

	log.Info("Deploying Device Plugin DaemonSet (image=%s, mgmt_port_vfs_count=%d)...", imageRef, mgmtPortVFsCount)
	if err := k8sClient.ApplyManifest([]byte(manifest)); err != nil {
		return fmt.Errorf("failed to apply Device Plugin DaemonSet: %w", err)
	}

	if err := k8sClient.WaitForPodsReady(DevicePluginNamespace, "app="+DevicePluginDaemonSetName, 3*time.Minute); err != nil {
		log.Warn("Warning: Device Plugin DaemonSet pods may not be ready: %v", err)
	} else {
		log.Info("✓ Device Plugin DaemonSet is ready")
	}

	return nil
}
