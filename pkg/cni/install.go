package cni

import (
	"fmt"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/containerengine"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

// InstallCNI installs the specified CNI on a cluster using the Kubernetes API.
// For OVN-Kubernetes the deployment mode (full / DPU-host / DPU) and network
// interfaces are determined automatically from the config.
//
// When DPU offloading is enabled and it is the DPU cluster, OVN-Kubernetes
// in DPU mode is automatically deployed after the primary CNI so the cluster
// has working pod networking (via flannel/kindnet) while still running the
// OVN datapath offload for the host cluster.
func (m *CNIManager) InstallCNI(cniType config.CNIType, clusterName string, k8sIP string) error {
	log.Info("\n=== Installing %s CNI on cluster %s ===", cniType, clusterName)

	switch cniType {
	case config.CNIFlannel:
		if err := m.installFlannel(clusterName); err != nil {
			return err
		}
	case config.CNIOVNKubernetes:
		if err := m.installOVNKubernetes(clusterName, k8sIP); err != nil {
			return err
		}
	case config.CNIKindnet:
		if m.config.IsKindMode() {
			log.Info("Kindnet is the default CNI for Kind clusters, no installation needed")
		} else {
			return fmt.Errorf("kindnet is not supported for cluster %s", clusterName)
		}
	default:
		return fmt.Errorf("unsupported CNI type: %s", cniType)
	}

	if cniType != config.CNIOVNKubernetes && m.config.DPUClusterNeedsOVNK(clusterName) {
		if !m.config.ShouldInstallOVNKubernetes() {
			log.Info("\n=== DPU offload enabled: generating OVN-Kubernetes DPU values on cluster %s ===", clusterName)
		} else {
			log.Info("\n=== DPU offload enabled: auto-deploying OVN-Kubernetes in DPU mode on cluster %s ===", clusterName)
		}
		if err := m.installOVNKubernetes(clusterName, k8sIP); err != nil {
			return fmt.Errorf("failed to install OVN-Kubernetes DPU mode on cluster %s: %w", clusterName, err)
		}
	}

	return nil
}

func (m *CNIManager) InstallAddon(addonType config.AddonType, clusterName string) error {
	log.Info("\n=== Installing addon %s on cluster %s ===", addonType, clusterName)

	switch addonType {
	case config.AddonMultus:
		return m.installMultus(clusterName)
	case config.AddonCertManager:
		return m.installCertManager(clusterName)
	case config.AddonWhereabouts:
		return m.installWhereabouts(clusterName)
	default:
		return fmt.Errorf("unsupported addon type: %s", addonType)
	}
}

func (m *CNIManager) InstallAddons(addons []config.AddonType, clusterName string) error {
	orderedAddons := resolveAddonInstallOrder(addons)
	for _, addon := range orderedAddons {
		if err := m.InstallAddon(addon, clusterName); err != nil {
			return err
		}
	}

	return nil
}

func resolveAddonInstallOrder(addons []config.AddonType) []config.AddonType {
	ordered := make([]config.AddonType, 0, len(addons))

	hasWhereabouts := false
	for _, addon := range addons {
		if addon == config.AddonWhereabouts {
			hasWhereabouts = true
			break
		}
	}

	// Install whereabouts before multus when both are explicitly configured.
	if hasWhereabouts {
		for _, addon := range addons {
			if addon == config.AddonWhereabouts {
				continue
			}
			if addon == config.AddonMultus {
				ordered = append(ordered, config.AddonWhereabouts)
			}
			ordered = append(ordered, addon)
		}
	} else {
		ordered = append(ordered, addons...)
	}

	return ordered
}

// BuildCNIImageWithRuntime returns a registry.BuildFunc that builds container
// images using the provided config, executor, and engine. The config is used
// to resolve the OVN-Kubernetes source path (--ovn-kubernetes-path override).
func BuildCNIImageWithRuntime(
	cfg *config.Config,
	cmdExec platform.CommandExecutor,
	engine containerengine.Engine,
) func(container config.RegistryContainerConfig) (string, error) {
	return func(container config.RegistryContainerConfig) (string, error) {
		cniType := config.CNIType(container.CNI)
		switch cniType {
		case config.CNIOVNKubernetes:
			localImage := container.Tag
			if err := BuildOVNKubernetesImageWithEngine(cfg, cmdExec, engine, localImage, ""); err != nil {
				return "", fmt.Errorf("failed to build OVN-Kubernetes image: %w", err)
			}
			return localImage, nil
		default:
			return "", fmt.Errorf("unsupported CNI type for image build: %s", cniType)
		}
	}
}

// RedeployCNI triggers a rolling restart of the CNI components on the specified
// cluster so that pods pick up the newly built image. Requires a Kubernetes
// client and CommandExecutor.
func (m *CNIManager) RedeployCNI(clusterName string) error {
	cniType := m.config.GetCNIType(clusterName)

	switch cniType {
	case config.CNIOVNKubernetes:
		return m.redeployOVNKubernetes(clusterName)
	default:
		log.Info("CNI %q does not support redeployment, skipping cluster %s", cniType, clusterName)
	}

	if cniType != config.CNIOVNKubernetes && m.config.DPUClusterNeedsOVNK(clusterName) {
		log.Info("DPU offload enabled: redeploying OVN-Kubernetes DPU mode on cluster %s", clusterName)
		return m.redeployOVNKubernetes(clusterName)
	}

	return nil
}
