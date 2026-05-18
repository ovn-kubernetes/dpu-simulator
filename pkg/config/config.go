// Package config provides configuration management for the DPU simulator.
//
// This package handles loading and parsing YAML configuration files,
// providing type-safe access to all configuration parameters.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ovn-kubernetes/dpu-simulator/lib/dpusim"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"

	"gopkg.in/yaml.v3"
)

// LoadConfig loads configuration from a YAML file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", path, err)
	}

	if err := cfg.validateAndSetDefaults(); err != nil {
		return nil, fmt.Errorf("config validation and set defaults failed: %w", err)
	}

	return &cfg, nil
}

// SetOVNKubernetesMode validates and stores how dpu-sim should handle
// OVN-Kubernetes Helm deployment.
func (c *Config) SetOVNKubernetesMode(mode string) error {
	switch mode {
	case "", OVNKubernetesModeInstall:
		c.OVNKubernetesMode = OVNKubernetesModeInstall
	case OVNKubernetesModeValuesOnly:
		c.OVNKubernetesMode = OVNKubernetesModeValuesOnly
	default:
		return fmt.Errorf("unsupported OVN-Kubernetes mode %q, expected %q or %q", mode, OVNKubernetesModeInstall, OVNKubernetesModeValuesOnly)
	}
	return nil
}

// ShouldInstallOVNKubernetes returns true when dpu-sim should run Helm for
// OVN-Kubernetes. The zero value preserves the historical install behavior.
func (c *Config) ShouldInstallOVNKubernetes() bool {
	return c.OVNKubernetesMode == "" || c.OVNKubernetesMode == OVNKubernetesModeInstall
}

// validate and set defaults checks that all mandatory fields in the configuration are set
// and sets default values for optional fields.
func (c *Config) validateAndSetDefaults() error {
	var errors []string

	// Validate networks
	for i, net := range c.Networks {
		if net.Name == "" {
			errors = append(errors, fmt.Sprintf("networks[%d]: 'name' is required", i))
		}
		if net.Type == "" {
			errors = append(errors, fmt.Sprintf("networks[%d] (%s): 'type' is required", i, net.Name))
		}
		if c.Networks[i].NICModel == "" {
			c.Networks[i].NICModel = "virtio"
		}

		if net.Type == HostToDpuNetworkType {
			if net.BridgeName != "" {
				errors = append(errors, fmt.Sprintf("networks[%d] (%s): 'bridge_name' is not allowed for type %s", i, net.Name, net.Type))
			}
			if net.Gateway != "" {
				errors = append(errors, fmt.Sprintf("networks[%d] (%s): 'gateway' is not allowed for type %s", i, net.Name, net.Type))
			}
			if net.SubnetMask != "" {
				errors = append(errors, fmt.Sprintf("networks[%d] (%s): 'subnet_mask' is not allowed for type %s", i, net.Name, net.Type))
			}
			if net.DHCPStart != "" {
				errors = append(errors, fmt.Sprintf("networks[%d] (%s): 'dhcp_start' is not allowed for type %s", i, net.Name, net.Type))
			}
			if net.DHCPEnd != "" {
				errors = append(errors, fmt.Sprintf("networks[%d] (%s): 'dhcp_end' is not allowed for type %s", i, net.Name, net.Type))
			}
			if net.Mode != "" {
				errors = append(errors, fmt.Sprintf("networks[%d] (%s): 'mode' is not allowed for type %s", i, net.Name, net.Type))
			}
			if net.UseOVS {
				errors = append(errors, fmt.Sprintf("networks[%d] (%s): 'use_ovs' is not allowed for type %s", i, net.Name, net.Type))
			}
			if net.AttachTo != "" {
				errors = append(errors, fmt.Sprintf("networks[%d] (%s): 'attach_to' is not allowed for type %s", i, net.Name, net.Type))
			}
			if c.Networks[i].NumPairs <= 0 {
				c.Networks[i].NumPairs = 1
			}
		} else {
			if net.BridgeName == "" {
				errors = append(errors, fmt.Sprintf("networks[%d] (%s): 'bridge_name' is required", i, net.Name))
			}
			if net.NumPairs > 0 {
				errors = append(errors, fmt.Sprintf("networks[%d] (%s): 'num_pairs' is not allowed for type %s", i, net.Name, net.Type))
			}
			if c.Networks[i].Mode == "" {
				c.Networks[i].Mode = "nat"
			}
			if !c.Networks[i].UseOVS {
				c.Networks[i].UseOVS = false
			}
			if c.Networks[i].AttachTo == "" {
				c.Networks[i].AttachTo = "any"
			}
		}
	}

	// Validate VMs
	for i, vm := range c.VMs {
		if vm.Name == "" {
			errors = append(errors, fmt.Sprintf("vms[%d]: 'name' is required", i))
		}
		if vm.Type == "" {
			errors = append(errors, fmt.Sprintf("vms[%d] (%s): 'type' is required", i, vm.Name))
		} else if vm.Type != HostType && vm.Type != DpuType {
			errors = append(errors, fmt.Sprintf("vms[%d] (%s): 'type' must be '%s' or '%s', got '%s'", i, vm.Name, HostType, DpuType, vm.Type))
		}
		if vm.K8sCluster == "" {
			errors = append(errors, fmt.Sprintf("vms[%d] (%s): 'k8s_cluster' is required", i, vm.Name))
		}
		if vm.K8sRole == "" {
			errors = append(errors, fmt.Sprintf("vms[%d] (%s): 'k8s_role' is required", i, vm.Name))
		}
		if vm.K8sNodeMAC == "" {
			errors = append(errors, fmt.Sprintf("vms[%d] (%s): 'k8s_node_mac' is required", i, vm.Name))
		}
		if vm.K8sNodeIP == "" {
			errors = append(errors, fmt.Sprintf("vms[%d] (%s): 'k8s_node_ip' is required", i, vm.Name))
		}
		if vm.Memory <= 0 {
			errors = append(errors, fmt.Sprintf("vms[%d] (%s): 'memory' must be greater than 0", i, vm.Name))
		}
		if vm.VCPUs <= 0 {
			errors = append(errors, fmt.Sprintf("vms[%d] (%s): 'vcpus' must be greater than 0", i, vm.Name))
		}
		if vm.DiskSize <= 0 {
			errors = append(errors, fmt.Sprintf("vms[%d] (%s): 'disk_size' must be greater than 0", i, vm.Name))
		}
		// If DPU type, host field should reference an existing host
		if vm.Type == DpuType && vm.Host == "" {
			errors = append(errors, fmt.Sprintf("vms[%d] (%s): 'host' is required for %s type VMs", i, vm.Name, DpuType))
		}
	}

	// TODO(vmctl): Extend vmctl commands to understand and operate on baremetal
	// nodes in addition to VM-backed nodes.
	// Validate BareMetal
	for i, node := range c.BareMetal {
		if node.Name == "" {
			errors = append(errors, fmt.Sprintf("baremetal[%d]: 'name' is required", i))
		}
		if node.Type == "" {
			c.BareMetal[i].Type = HostType
			node.Type = HostType
		} else if node.Type != HostType && node.Type != DpuType {
			errors = append(errors, fmt.Sprintf("baremetal[%d] (%s): 'type' must be '%s' or '%s', got '%s'", i, node.Name, HostType, DpuType, node.Type))
		}
		if node.K8sCluster == "" {
			errors = append(errors, fmt.Sprintf("baremetal[%d] (%s): 'k8s_cluster' is required", i, node.Name))
		}
		if node.K8sRole == "" {
			errors = append(errors, fmt.Sprintf("baremetal[%d] (%s): 'k8s_role' is required", i, node.Name))
		}
		if node.MgmtIP == "" {
			errors = append(errors, fmt.Sprintf("baremetal[%d] (%s): 'mgmt_ip' is required", i, node.Name))
		}
		if node.NodeIP == "" {
			errors = append(errors, fmt.Sprintf("baremetal[%d] (%s): 'node_ip' is required", i, node.Name))
		}
		if node.Type == DpuType && node.Host == "" {
			errors = append(errors, fmt.Sprintf("baremetal[%d] (%s): 'host' is required for type '%s'", i, node.Name, DpuType))
		}

		if node.BootstrapSSH != nil {
			if node.BootstrapSSH.User == "" {
				if c.SSH.User != "" {
					c.BareMetal[i].BootstrapSSH.User = c.SSH.User
				} else {
					c.BareMetal[i].BootstrapSSH.User = "root"
				}
			}
			if node.BootstrapSSH.KeyPath != "" {
				expanded, err := expandTilde(node.BootstrapSSH.KeyPath)
				if err != nil {
					errors = append(errors, fmt.Sprintf("baremetal[%d] (%s): bootstrap_ssh.key_path expand failed: %v", i, node.Name, err))
				} else {
					c.BareMetal[i].BootstrapSSH.KeyPath = expanded
				}
			}
			if node.BootstrapSSH.KeyPath == "" && node.BootstrapSSH.Password == "" {
				errors = append(errors, fmt.Sprintf("baremetal[%d] (%s): bootstrap_ssh requires one of 'key_path' or 'password'", i, node.Name))
			}
		}

		if node.Bootc != nil {
			if node.Bootc.Enabled {
				strategy := node.Bootc.Strategy
				if strategy == "" {
					strategy = "switch"
					c.BareMetal[i].Bootc.Strategy = strategy
				}
				if strategy != "switch" && strategy != "upgrade" {
					errors = append(errors, fmt.Sprintf("baremetal[%d] (%s): bootc.strategy must be 'switch' or 'upgrade'", i, node.Name))
				}
				if strategy == "switch" && node.Bootc.ImageRef == "" {
					errors = append(errors, fmt.Sprintf("baremetal[%d] (%s): bootc.image_ref is required when bootc.strategy is 'switch'", i, node.Name))
				}
				if node.Bootc.Transport == "" {
					c.BareMetal[i].Bootc.Transport = "registry"
				}
				if node.Bootc.SoftReboot != "" && node.Bootc.SoftReboot != "auto" && node.Bootc.SoftReboot != "required" {
					errors = append(errors, fmt.Sprintf("baremetal[%d] (%s): bootc.soft_reboot must be 'auto' or 'required'", i, node.Name))
				}
			}
		}
	}

	// Validate operating system (required for VM mode)
	if len(c.VMs) > 0 {
		if c.OperatingSystem.ImageURL == "" && c.OperatingSystem.ImageRef == "" {
			errors = append(errors, "VMs are defined, operating_system: one of 'image_url' or 'image_ref' is required")
		}
		if c.OperatingSystem.ImageURL != "" && c.OperatingSystem.ImageRef != "" {
			errors = append(errors, "VMs are defined, operating_system: 'image_url' and 'image_ref' are mutually exclusive")
		}
		if c.OperatingSystem.ImageName == "" {
			errors = append(errors, "VMs are defined, operating_system: 'image_name' is required")
		}
	}

	if c.SSH.User == "" {
		c.SSH.User = "root"
	}

	if c.SSH.Password == "" {
		c.SSH.Password = "redhat"
	}

	// Expand tilde in SSH key path
	if c.SSH.KeyPath != "" {
		expanded, err := expandTilde(c.SSH.KeyPath)
		if err != nil {
			errors = append(errors, fmt.Sprintf("ssh.key_path: failed to expand path: %v", err))
		} else {
			c.SSH.KeyPath = expanded
		}
	}

	// Validate Kind nodes (if Kind mode)
	if c.Kind != nil {
		kindNodeNames := make(map[string]bool)
		for i, node := range c.Kind.Nodes {
			if node.Name == "" {
				errors = append(errors, fmt.Sprintf("kind.nodes[%d]: 'name' is required", i))
			} else {
				kindNodeNames[node.Name] = true
			}
			if node.K8sRole != "" && node.K8sRole != "control-plane" && node.K8sRole != "worker" {
				errors = append(errors, fmt.Sprintf("kind.nodes[%d] (%s): 'k8s_role' must be 'control-plane' or 'worker', got '%s'", i, node.Name, node.K8sRole))
			}
			if node.K8sCluster == "" {
				errors = append(errors, fmt.Sprintf("kind.nodes[%d] (%s): 'k8s_cluster' is required", i, node.Name))
			}
			if node.Type != "" && node.Type != HostType && node.Type != DpuType {
				errors = append(errors, fmt.Sprintf("kind.nodes[%d] (%s): 'type' must be 'host' or 'dpu', got '%s'", i, node.Name, node.Type))
			}
			if node.Type == "dpu" && node.Host == "" {
				errors = append(errors, fmt.Sprintf("kind.nodes[%d] (%s): 'host' is required for type 'dpu'", i, node.Name))
			}
		}
		// Validate Kind host references and cluster names
		clusterNames := make(map[string]bool)
		for _, cluster := range c.Kubernetes.Clusters {
			clusterNames[cluster.Name] = true
		}
		for i, node := range c.Kind.Nodes {
			if node.K8sCluster != "" && !clusterNames[node.K8sCluster] {
				errors = append(errors, fmt.Sprintf("kind.nodes[%d] (%s): 'k8s_cluster' %q not found in kubernetes.clusters", i, node.Name, node.K8sCluster))
			}
			if node.Type == "dpu" && node.Host != "" && !kindNodeNames[node.Host] {
				errors = append(errors, fmt.Sprintf("kind.nodes[%d] (%s): 'host' references non-existent node '%s'", i, node.Name, node.Host))
			}
		}
	}

	// Set default Kubernetes version if not specified
	if c.Kubernetes.Version == "" {
		c.Kubernetes.Version = "1.33"
	}

	if c.Kubernetes.KubeconfigDir == "" {
		c.Kubernetes.KubeconfigDir = "kubeconfig"
	}

	// Validate Kubernetes clusters
	for i, cluster := range c.Kubernetes.Clusters {
		if cluster.Name == "" {
			errors = append(errors, fmt.Sprintf("kubernetes.clusters[%d]: 'name' is required", i))
		}
		if c.Kubernetes.Clusters[i].PodCIDR == "" {
			c.Kubernetes.Clusters[i].PodCIDR = "10.244.0.0/16"
		}
		if c.Kubernetes.Clusters[i].ServiceCIDR == "" {
			c.Kubernetes.Clusters[i].ServiceCIDR = "10.245.0.0/16"
		}
		if c.Kubernetes.Clusters[i].CNI == "" {
			errors = append(errors, fmt.Sprintf("kubernetes.clusters[%d]: 'cni' is required", i))
		} else if c.Kubernetes.Clusters[i].CNI != CNIOVNKubernetes && c.Kubernetes.Clusters[i].CNI != CNIFlannel && c.Kubernetes.Clusters[i].CNI != CNIKindnet {
			errors = append(errors, fmt.Sprintf("kubernetes.clusters[%d]: 'cni' must be 'ovn-kubernetes', 'flannel', or 'kindnet', got '%s'", i, c.Kubernetes.Clusters[i].CNI))
		}

		normalizedAddons, duplicates, addonErrs := validateAndNormalizeAddons(cluster.Addons)
		for _, err := range addonErrs {
			errors = append(errors, fmt.Sprintf("kubernetes.clusters[%d] (%s): %s", i, cluster.Name, err))
		}
		for _, dup := range duplicates {
			log.Warn("kubernetes.clusters[%d] (%s): duplicate addon %q found, keeping first occurrence", i, cluster.Name, dup)
		}
		c.Kubernetes.Clusters[i].Addons = normalizedAddons
	}

	// Validate registry configuration
	if c.Registry != nil {
		for i, endpoint := range c.Registry.InsecureEndpoints {
			normalized := strings.TrimSpace(endpoint)
			c.Registry.InsecureEndpoints[i] = normalized
			if normalized == "" {
				errors = append(errors, fmt.Sprintf("registry.insecure_endpoints[%d]: value must not be empty", i))
				continue
			}
			if err := validateRegistryEndpoint(normalized); err != nil {
				errors = append(errors, fmt.Sprintf("registry.insecure_endpoints[%d]: %v", i, err))
			}
		}

		for i, container := range c.Registry.Containers {
			if container.Name == "" {
				errors = append(errors, fmt.Sprintf("registry.containers[%d]: 'name' is required", i))
			}
			if container.CNI == "" {
				errors = append(errors, fmt.Sprintf("registry.containers[%d] (%s): 'cni' is required", i, container.Name))
			} else if CNIType(container.CNI) != CNIOVNKubernetes {
				errors = append(errors, fmt.Sprintf("registry.containers[%d] (%s): 'cni' must be 'ovn-kubernetes', got '%s'", i, container.Name, container.CNI))
			}
			if container.Tag == "" {
				errors = append(errors, fmt.Sprintf("registry.containers[%d] (%s): 'tag' is required", i, container.Name))
			}
		}
	}

	if c.IsRegistryImageBuildOnly() {
		mode, modeErr := c.GetDeploymentMode()
		if modeErr == nil && mode != KindDeploymentMode {
			errors = append(errors, "registry.enabled=false with registry.containers is only supported in Kind mode")
		}
	}

	// Validate DPU host references exist
	if len(c.VMs) > 0 {
		hostNames := make(map[string]bool)
		for _, vm := range c.VMs {
			if vm.Type == HostType {
				hostNames[vm.Name] = true
			}
		}
		for _, node := range c.BareMetal {
			if node.Type == HostType {
				hostNames[node.Name] = true
			}
		}
		for i, vm := range c.VMs {
			if vm.Type == DpuType && vm.Host != "" {
				if !hostNames[vm.Host] {
					errors = append(errors, fmt.Sprintf("vms[%d] (%s): 'host' references non-existent host '%s'", i, vm.Name, vm.Host))
				}
			}
		}
		for i, node := range c.BareMetal {
			if node.Type == DpuType && node.Host != "" && !hostNames[node.Host] {
				errors = append(errors, fmt.Sprintf("baremetal[%d] (%s): 'host' references non-existent host '%s'", i, node.Name, node.Host))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation errors:\n  - %s", strings.Join(errors, "; "))
	}

	return nil
}

func validateAndNormalizeAddons(addons []AddonType) ([]AddonType, []AddonType, []string) {
	seen := make(map[AddonType]struct{}, len(addons))
	normalized := make([]AddonType, 0, len(addons))
	duplicates := make([]AddonType, 0)
	var errs []string

	for idx, addon := range addons {
		switch addon {
		case AddonMultus, AddonCertManager, AddonWhereabouts:
			// valid
		default:
			errs = append(errs, fmt.Sprintf("addons[%d]: unsupported addon %q", idx, addon))
			continue
		}

		if _, exists := seen[addon]; exists {
			duplicates = append(duplicates, addon)
			continue
		}

		seen[addon] = struct{}{}
		normalized = append(normalized, addon)
	}

	return normalized, duplicates, errs
}

func validateRegistryEndpoint(endpoint string) error {
	if strings.Contains(endpoint, "://") {
		return fmt.Errorf("must be in host:port format without URL scheme")
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("must be a valid host:port endpoint")
	}
	if host == "" {
		return fmt.Errorf("host is required")
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return fmt.Errorf("port must be an integer between 1 and 65535")
	}
	return nil
}

// GetDeploymentMode determines the deployment mode based on configuration
func (c *Config) GetDeploymentMode() (string, error) {
	hasVMs := len(c.VMs) > 0 || len(c.BareMetal) > 0
	hasKind := c.Kind != nil && len(c.Kind.Nodes) > 0

	if hasKind && hasVMs {
		return "", fmt.Errorf("both 'vms' and 'kind' sections found in config - please use only one deployment mode")
	} else if hasKind {
		return KindDeploymentMode, nil
	} else if hasVMs {
		return VMDeploymentMode, nil
	}

	return "", fmt.Errorf("neither 'vms' nor 'kind' section found in config")
}

// IsKindMode returns true if the configuration is for Kind mode
func (c *Config) IsKindMode() bool {
	mode, _ := c.GetDeploymentMode()
	return mode == KindDeploymentMode
}

// IsVMMode returns true if the configuration is for VM mode
func (c *Config) IsVMMode() bool {
	mode, _ := c.GetDeploymentMode()
	return mode == VMDeploymentMode
}

// GetHostDPUMappings returns all host-to-DPU mappings from VM configuration
func (c *Config) GetHostDPUMappings() []HostDPUMapping {
	// Build map of hosts by name for lookup
	hosts := make(map[string]VMConfig)
	for _, vm := range c.VMs {
		if vm.Type == HostType {
			hosts[vm.Name] = vm
		}
	}

	// Build map of host name -> DPU connections
	hostConnections := make(map[string][]DPUConnection)
	for _, vm := range c.VMs {
		if vm.Type == DpuType && vm.Host != "" {
			if _, ok := hosts[vm.Host]; ok {
				conn := DPUConnection{
					DPU: vm,
					Link: HostDPULink{
						NetworkName: fmt.Sprintf("h2d-%s-%s", vm.Host, vm.Name),
					},
				}
				hostConnections[vm.Host] = append(hostConnections[vm.Host], conn)
			}
		}
	}

	var mappings []HostDPUMapping
	for hostName, connections := range hostConnections {
		mappings = append(mappings, HostDPUMapping{
			Host:        hosts[hostName],
			Connections: connections,
		})
	}

	return mappings
}

// GetClusterRoleMapping returns a mapping of cluster names to roles and their VMs.
// The returned map has cluster names as keys, and values are ClusterRoleMapping
// which maps roles (master/worker) to slices of VMConfig.
// Example structure: {"cluster1": {"master": [vm1], "worker": [vm2, vm3]}}
func (c *Config) GetClusterRoleMapping() map[string]ClusterRoleMapping {
	result := make(map[string]ClusterRoleMapping)

	for _, vm := range c.VMs {
		clusterName := vm.K8sCluster
		role := ClusterRole(vm.K8sRole)

		if _, ok := result[clusterName]; !ok {
			result[clusterName] = make(ClusterRoleMapping)
		}

		result[clusterName][role] = append(result[clusterName][role], vm)
	}

	return result
}

// GetBareMetalClusterRoleMapping returns a mapping of cluster names to roles and their baremetal nodes.
func (c *Config) GetBareMetalClusterRoleMapping() map[string]BareMetalClusterRoleMapping {
	result := make(map[string]BareMetalClusterRoleMapping)

	for _, node := range c.BareMetal {
		clusterName := node.K8sCluster
		role := ClusterRole(node.K8sRole)

		if _, ok := result[clusterName]; !ok {
			result[clusterName] = make(BareMetalClusterRoleMapping)
		}

		result[clusterName][role] = append(result[clusterName][role], node)
	}

	return result
}

// GetClusterNames returns all cluster names from configuration
func (c *Config) GetClusterNames() []string {
	names := make([]string, len(c.Kubernetes.Clusters))
	for i, cluster := range c.Kubernetes.Clusters {
		names[i] = cluster.Name
	}
	return names
}

// GetClusterConfig returns the cluster configuration by name
func (c *Config) GetClusterConfig(name string) *ClusterConfig {
	for i := range c.Kubernetes.Clusters {
		if c.Kubernetes.Clusters[i].Name == name {
			return &c.Kubernetes.Clusters[i]
		}
	}
	return nil
}

// GetCNIType returns the CNI type for a cluster
func (c *Config) GetCNIType(clusterName string) CNIType {
	cluster := c.GetClusterConfig(clusterName)
	if cluster == nil {
		// Return default based on mode
		if c.IsKindMode() {
			return "kindnet"
		}
		return "flannel"
	}
	return cluster.CNI
}

// GetKindNodesForCluster returns Kind nodes that belong to the given cluster, in config file order.
func (c *Config) GetKindNodesForCluster(clusterName string) []KindNodeConfig {
	if c.Kind == nil {
		return nil
	}
	var out []KindNodeConfig
	for _, node := range c.Kind.Nodes {
		if node.K8sCluster == clusterName {
			out = append(out, node)
		}
	}
	return out
}

// GetKindNodeContainerName returns the Docker container name for a Kind node.
// clusterName is the Kind cluster name
// nodeName is the config "name" (also the dpu-sim.org/node-name label) of the node.
// Kind names: first control-plane = <cluster>-control-plane, then control-plane2, ...;
// first worker = <cluster>-worker, then worker2, worker3, ...
func (c *Config) GetKindNodeContainerName(clusterName string, nodeName string) string {
	nodes := c.GetKindNodesForCluster(clusterName)
	cpIdx, workerIdx := 0, 0
	for _, n := range nodes {
		role := n.K8sRole
		var name string
		switch role {
		case "control-plane":
			cpIdx++
			if cpIdx == 1 {
				name = clusterName + "-control-plane"
			} else {
				name = fmt.Sprintf("%s-control-plane%d", clusterName, cpIdx)
			}
		case "worker":
			workerIdx++
			if workerIdx == 1 {
				name = clusterName + "-worker"
			} else {
				name = fmt.Sprintf("%s-worker%d", clusterName, workerIdx)
			}
		default:
			return ""
		}
		if n.Name == nodeName {
			return name
		}
	}
	return ""
}

// GetKindControlPlaneCount returns the number of control-plane nodes in Kind config for a cluster.
func (c *Config) GetKindControlPlaneCount(clusterName string) int {
	if c.Kind == nil {
		return 0
	}
	count := 0
	for _, node := range c.Kind.Nodes {
		if node.K8sCluster == clusterName && node.K8sRole == "control-plane" {
			count++
		}
	}
	return count
}

// GetKindWorkerCount returns the number of worker nodes in Kind config for a cluster.
func (c *Config) GetKindWorkerCount(clusterName string) int {
	if c.Kind == nil {
		return 0
	}
	count := 0
	for _, node := range c.Kind.Nodes {
		if node.K8sCluster == clusterName && (node.K8sRole == "worker" || node.K8sRole == "") {
			count++
		}
	}
	return count
}

// HostDPUPair describes a Host and DPU worker pair. HostNode and DPUNode are the
// K8s node names (Kind container names in Kind mode, VM names in VM mode).
type HostDPUPair struct {
	HostNode string
	DPUNode  string
}

// GetHostDPUPairs returns host-DPU worker pairs. When dpuClusterName is empty
// all pairs are returned; otherwise only pairs whose DPU belongs to the given
// cluster are included. Works for both Kind and VM modes.
func (c *Config) GetHostDPUPairs(dpuClusterName string) []HostDPUPair {
	if c.IsVMMode() {
		return c.getHostDPUPairsVM(dpuClusterName)
	}
	return c.getHostDPUPairsKind(dpuClusterName)
}

func (c *Config) getHostDPUPairsVM(dpuClusterName string) []HostDPUPair {
	var pairs []HostDPUPair
	for _, vm := range c.VMs {
		if vm.Type != DpuType || vm.Host == "" {
			continue
		}
		if dpuClusterName != "" && vm.K8sCluster != dpuClusterName {
			continue
		}
		pairs = append(pairs, HostDPUPair{
			HostNode: vm.Host,
			DPUNode:  vm.Name,
		})
	}
	return pairs
}

func (c *Config) getHostDPUPairsKind(dpuClusterName string) []HostDPUPair {
	if c.Kind == nil {
		return nil
	}
	hostByName := make(map[string]KindNodeConfig)
	for _, node := range c.Kind.Nodes {
		if node.Type == HostType {
			hostByName[node.Name] = node
		}
	}
	var pairs []HostDPUPair
	for _, node := range c.Kind.Nodes {
		if node.Type != DpuType || node.Host == "" {
			continue
		}
		if dpuClusterName != "" && node.K8sCluster != dpuClusterName {
			continue
		}
		host, ok := hostByName[node.Host]
		if !ok {
			continue
		}
		hostContainer := c.GetKindNodeContainerName(host.K8sCluster, host.Name)
		dpuContainer := c.GetKindNodeContainerName(node.K8sCluster, node.Name)
		if hostContainer == "" || dpuContainer == "" {
			continue
		}
		pairs = append(pairs, HostDPUPair{
			HostNode: hostContainer,
			DPUNode:  dpuContainer,
		})
	}
	return pairs
}

// IsOffloadDPU returns true if DPU offloading is enabled.
// When true, OVN-Kubernetes is deployed in DPU/DPU-Host mode instead of Full mode.
func (c *Config) IsOffloadDPU() bool {
	return c.Kubernetes.OffloadDPU
}

// DPUClusterNeedsOVNK returns true when clusterName is the DPU cluster in an
// offload topology whose host cluster uses OVN-Kubernetes. In that scenario
// the DPU cluster must deploy OVN-K in DPU mode alongside its own primary CNI
// (e.g. flannel) so the host cluster's networking can be offloaded.
func (c *Config) DPUClusterNeedsOVNK(clusterName string) bool {
	if !c.IsOffloadDPU() || !c.IsDPUCluster(clusterName) {
		return false
	}
	hostCluster := c.GetDPUHostClusterName()
	return hostCluster != "" && c.GetCNIType(hostCluster) == CNIOVNKubernetes
}

// IsDPUCluster returns true if the named cluster contains DPU-type nodes.
func (c *Config) IsDPUCluster(clusterName string) bool {
	for _, vm := range c.VMs {
		if vm.K8sCluster == clusterName && vm.Type == DpuType {
			return true
		}
	}
	if c.Kind != nil {
		for _, node := range c.Kind.Nodes {
			if node.K8sCluster == clusterName && node.Type == DpuType {
				return true
			}
		}
	}
	return false
}

// GetDPUClusterName returns the name of the cluster that contains DPU-type nodes,
// or empty string if none.
func (c *Config) GetDPUClusterName() string {
	for _, cluster := range c.Kubernetes.Clusters {
		if c.IsDPUCluster(cluster.Name) {
			return cluster.Name
		}
	}
	return ""
}

// GetDPUHostClusterName returns the name of the cluster that contains host nodes
// paired with DPUs.
func (c *Config) GetDPUHostClusterName() string {
	dpuCluster := c.GetDPUClusterName()
	if dpuCluster == "" {
		return ""
	}
	for _, cluster := range c.Kubernetes.Clusters {
		if cluster.Name != dpuCluster {
			return cluster.Name
		}
	}
	return ""
}

// GetDPUHostNodeNames returns the K8s node names for DPU-host nodes in the
// given cluster. These are host-type nodes that have a corresponding DPU in
// another cluster.
func (c *Config) GetDPUHostNodeNames(clusterName string) []string {
	dpuHostNames := make(map[string]bool)
	for _, vm := range c.VMs {
		if vm.Type == DpuType && vm.Host != "" {
			dpuHostNames[vm.Host] = true
		}
	}
	if c.Kind != nil {
		for _, node := range c.Kind.Nodes {
			if node.Type == DpuType && node.Host != "" {
				dpuHostNames[node.Host] = true
			}
		}
	}

	var names []string
	if c.IsVMMode() {
		for _, vm := range c.VMs {
			if vm.K8sCluster == clusterName && vm.Type == HostType && dpuHostNames[vm.Name] {
				names = append(names, vm.Name)
			}
		}
	} else if c.Kind != nil {
		for _, node := range c.Kind.Nodes {
			if node.K8sCluster == clusterName && node.Type == HostType && dpuHostNames[node.Name] {
				containerName := c.GetKindNodeContainerName(clusterName, node.Name)
				if containerName != "" {
					names = append(names, containerName)
				}
			}
		}
	}
	return names
}

// GetDPUNodeNames returns the K8s node names for DPU-type nodes in the given cluster.
func (c *Config) GetDPUNodeNames(clusterName string) []string {
	var names []string
	if c.IsVMMode() {
		for _, vm := range c.VMs {
			if vm.K8sCluster == clusterName && vm.Type == DpuType {
				names = append(names, vm.Name)
			}
		}
	} else if c.Kind != nil {
		for _, node := range c.Kind.Nodes {
			if node.K8sCluster == clusterName && node.Type == DpuType {
				containerName := c.GetKindNodeContainerName(clusterName, node.Name)
				if containerName != "" {
					names = append(names, containerName)
				}
			}
		}
	}
	return names
}

// GetNetworkByType returns the network configuration by type (e.g., "mgmt" or "k8s")
func (c *Config) GetNetworkByType(networkType string) *NetworkConfig {
	for i := range c.Networks {
		if c.Networks[i].Type == networkType {
			return &c.Networks[i]
		}
	}
	return nil
}

// PrefixLenFromSubnetMask returns the CIDR prefix length (e.g. 24) for a subnet mask like "255.255.255.0".
func PrefixLenFromSubnetMask(subnetMask string) (int, error) {
	ip := net.ParseIP(subnetMask)
	if ip == nil {
		return 0, fmt.Errorf("invalid subnet mask %q", subnetMask)
	}
	ip = ip.To4()
	if ip == nil {
		return 0, fmt.Errorf("subnet mask %q is not IPv4", subnetMask)
	}
	ones, _ := net.IPv4Mask(ip[0], ip[1], ip[2], ip[3]).Size()
	return ones, nil
}

// GetHostToDpuNetwork returns the HostToDpu network config, or nil if not configured.
func (c *Config) GetHostToDpuNetwork() *NetworkConfig {
	for i := range c.Networks {
		if c.Networks[i].Type == HostToDpuNetworkType {
			return &c.Networks[i]
		}
	}
	return nil
}

// GetHostToDpuNumPairs returns the number of data channels per host-DPU pair.
// Returns 1 if no HostToDpu network is configured.
func (c *Config) GetHostToDpuNumPairs() int {
	net := c.GetHostToDpuNetwork()
	if net == nil || net.NumPairs <= 0 {
		return 1
	}
	return net.NumPairs
}

// GetNetworkByName returns the network configuration by name
func (c *Config) GetNetworkByName(name string) *NetworkConfig {
	for i := range c.Networks {
		if c.Networks[i].Name == name {
			return &c.Networks[i]
		}
	}
	return nil
}

// HasRegistry returns true if the configuration includes a local registry
func (c *Config) HasRegistry() bool {
	return c.Registry != nil && len(c.Registry.Containers) > 0
}

// IsRegistryEnabled returns true when local registry management is enabled.
//
// Behavior:
//   - false when no registry section is present
//   - true when registry section is present and enabled is unset
//   - value of registry.enabled when explicitly set
func (c *Config) IsRegistryEnabled() bool {
	if c.Registry == nil {
		return false
	}
	if c.Registry.Enabled == nil {
		return true
	}
	return *c.Registry.Enabled
}

// IsRegistryImageBuildOnly is true when registry.containers defines images to
// build but registry.enabled is false. In Kind mode, those images are built and
// loaded into clusters instead of being pushed to a local registry.
func (c *Config) IsRegistryImageBuildOnly() bool {
	return c.HasRegistry() && !c.IsRegistryEnabled()
}

// ClusterNeedsOVNKubernetesImage reports whether clusterName installs or runs
// an OVN-Kubernetes image (primary CNI or DPU-mode OVN on the DPU cluster).
func (c *Config) ClusterNeedsOVNKubernetesImage(clusterName string) bool {
	clusterCfg := c.GetClusterConfig(clusterName)
	if clusterCfg == nil {
		return false
	}
	if clusterCfg.CNI == CNIOVNKubernetes {
		return true
	}
	return c.DPUClusterNeedsOVNK(clusterName)
}

// KindNodeLocalImageRef returns an image reference that matches how containerd
// stores images after "kind load image-archive" for short names: the node shows
// localhost/<repository>:<tag>, while an unqualified "repo:tag" in a pod spec is
// resolved to docker.io/library/repo:tag — a different reference, which forces
// an unwanted pull.
func KindNodeLocalImageRef(image string) string {
	image = strings.TrimSpace(image)
	if image == "" || strings.HasPrefix(image, "localhost/") {
		return image
	}
	repo, tag := splitContainerImageRef(image)
	if imageRefRepoLooksLikeRegistry(repo) {
		return image
	}
	return "localhost/" + repo + ":" + tag
}

// splitContainerImageRef splits a container image reference into repository and tag.
func splitContainerImageRef(image string) (repo, tag string) {
	lastColon := strings.LastIndex(image, ":")
	lastSlash := strings.LastIndex(image, "/")
	if lastColon > lastSlash && lastColon >= 0 {
		return image[:lastColon], image[lastColon+1:]
	}
	return image, "latest"
}

// imageRefRepoLooksLikeRegistry returns true if the repository looks like a registry.
func imageRefRepoLooksLikeRegistry(repo string) bool {
	first := repo
	if i := strings.Index(repo, "/"); i >= 0 {
		first = repo[:i]
	}
	if strings.EqualFold(first, "localhost") {
		return true
	}
	if strings.Contains(first, ".") {
		return true
	}
	if strings.Contains(first, ":") {
		return true
	}
	return false
}

// OvnKubernetesImageForHelm returns the image reference passed to OVN-Kubernetes Helm.
// defaultImage is used when no registry container entry targets ovn-kubernetes.
func (c *Config) OvnKubernetesImageForHelm(defaultImage string) string {
	rc := c.GetRegistryContainerForCNI(CNIOVNKubernetes)
	if rc == nil {
		return defaultImage
	}
	if c.IsRegistryEnabled() {
		return c.GetRegistryImageRef(rc.Tag)
	}
	if c.IsKindMode() {
		if c.IsRegistryImageBuildOnly() {
			return KindNodeLocalImageRef(rc.Tag)
		}
		return rc.Tag
	}
	return c.GetRegistryImageRef(rc.Tag)
}

// GetRegistryInsecureEndpoints returns the node-side registry endpoints
// that should be configured as insecure HTTP registries.
//
// Behavior:
//   - when local registry management is disabled, return no endpoints
//   - when registry.insecure_endpoints is set, return that list
//   - otherwise fall back to GetRegistryNodeEndpoint()
func (c *Config) GetRegistryInsecureEndpoints() []string {
	if !c.IsRegistryEnabled() {
		return nil
	}

	if c.Registry != nil && len(c.Registry.InsecureEndpoints) > 0 {
		out := make([]string, 0, len(c.Registry.InsecureEndpoints))
		seen := make(map[string]struct{}, len(c.Registry.InsecureEndpoints))
		for _, endpoint := range c.Registry.InsecureEndpoints {
			normalized := strings.TrimSpace(endpoint)
			if normalized == "" {
				continue
			}
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			out = append(out, normalized)
		}
		if len(out) > 0 {
			return out
		}
	}

	return []string{c.GetRegistryNodeEndpoint()}
}

// GetRegistryContainerForCNI returns the registry container config for a
// given CNI type, or nil if none is configured.
func (c *Config) GetRegistryContainerForCNI(cniType CNIType) *RegistryContainerConfig {
	if c.Registry == nil {
		return nil
	}
	for i := range c.Registry.Containers {
		if CNIType(c.Registry.Containers[i].CNI) == cniType {
			return &c.Registry.Containers[i]
		}
	}
	return nil
}

// GetRegistryNodeEndpoint returns the registry address used in image
// references that cluster nodes will pull. In Kind mode this is
// localhost:<port> (containerd is configured to redirect pulls to the
// registry container on the Docker network). In VM mode it is the host's
// gateway IP on the management bridge.
func (c *Config) GetRegistryNodeEndpoint() string {
	if c.IsVMMode() {
		// VM mode: use the host's IP on the management network (gateway address)
		mgmtNet := c.GetNetworkByType(MgmtNetworkName)
		if mgmtNet != nil && mgmtNet.Gateway != "" {
			return fmt.Sprintf("%s:%s", mgmtNet.Gateway, DefaultRegistryPort)
		}
	}
	return fmt.Sprintf("localhost:%s", DefaultRegistryPort)
}

// GetRegistryContainerEndpoint returns the registry's actual address on
// the Docker network (container-name:port). Used by Kind containerd host
// configs to redirect pulls from localhost to the registry container.
func (c *Config) GetRegistryContainerEndpoint() string {
	return fmt.Sprintf("%s:%s", DefaultRegistryContainerName, DefaultRegistryPort)
}

// GetRegistryImageRef returns the image reference for a given registry tag
func (c *Config) GetRegistryImageRef(registryTag string) string {
	return fmt.Sprintf("%s/%s", c.GetRegistryNodeEndpoint(), registryTag)
}

// GetRegistryLocalImageRef returns the local image reference for a given registry tag
// (e.g. "localhost:5000/ovn-kube:dpu-sim").
func (c *Config) GetRegistryLocalImageRef(registryTag string) string {
	return fmt.Sprintf("%s/%s", c.GetRegistryLocalEndpoint(), registryTag)
}

// GetRegistryEndpoint returns the registry address reachable from the host
// (e.g. "localhost:5000"). Used for pushing images.
func (c *Config) GetRegistryLocalEndpoint() string {
	return fmt.Sprintf("localhost:%s", DefaultRegistryPort)
}

func (c *Config) GetRegistryPort() string {
	return DefaultRegistryPort
}

func (c *Config) GetRegistryImage() string {
	return DefaultRegistryImage
}

func (c *Config) GetRegistryContainerName() string {
	return DefaultRegistryContainerName
}

// expandTilde expands a leading ~ in a path to the user's home directory
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	if path == "~" {
		return home, nil
	}

	// Handle ~/path format
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:]), nil
	}

	// Just ~ prefix without slash - return as is (edge case like ~username not supported)
	return path, nil
}

// ClustersOrderedForInstall returns a copy of the configured clusters ordered
// so that DPU host clusters come before DPU clusters. This ensures the host
// cluster's service account secret exists before the DPU cluster tries to
// fetch it. When DPU offloading is disabled the original order is preserved.
func (c *Config) ClustersOrderedForInstall() []ClusterConfig {
	if !c.IsOffloadDPU() {
		return c.Kubernetes.Clusters
	}
	ordered := make([]ClusterConfig, 0, len(c.Kubernetes.Clusters))
	var dpuClusters []ClusterConfig
	for _, cl := range c.Kubernetes.Clusters {
		if c.IsDPUCluster(cl.Name) {
			dpuClusters = append(dpuClusters, cl)
		} else {
			ordered = append(ordered, cl)
		}
	}
	return append(ordered, dpuClusters...)
}

// GatewayInterfaces returns the gateway interface name for the ovnkube-node
// daemonset on the given cluster.
func (c *Config) GatewayInterfaces(clusterName string) string {
	gatewayIf := K8sNetworkName
	if c.IsKindMode() {
		gatewayIf = KindK8sNetworkName
	}
	return gatewayIf
}

// DPUHostGatewayInterface returns the gateway interface name for DPU-Host mode
func (c *Config) DPUHostGatewayInterface() string {
	return dpusim.HostGatewayInterface
}

// DPUHostGatewayRepresentorInterface returns the DPU-side representor for the
// host gateway interface.
func (c *Config) DPUHostGatewayRepresentorInterface() string {
	return dpusim.HostGatewayPeerInterface
}

// DPUHostManagementPortNetDevName returns the management port netdev name for DPU mode
func (c *Config) DPUHostManagementPortNetDevName() string {
	return dpusim.MgmtPortNetDevName
}
