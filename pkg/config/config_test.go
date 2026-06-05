package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestLoadConfig(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
networks:
  - name: "mgmt-network"
    type: "mgmt"
    bridge_name: "virbr-mgmt"
    gateway: "192.168.120.1"
    subnet_mask: "255.255.255.0"
    dhcp_start: "192.168.120.10"
    dhcp_end: "192.168.120.100"
    mode: "nat"
    nic_model: "virtio"

vms:
  - name: "master-1"
    type: "host"
    k8s_cluster: "cluster-1"
    k8s_role: "master"
    k8s_node_mac: "52:54:00:00:01:11"
    k8s_node_ip: "192.168.123.11"
    memory: 4096
    vcpus: 2
    disk_size: 20

operating_system:
  image_url: https://example.com/fedora.qcow2
  image_name: "Fedora-x86_64.qcow2"

ssh:
  user: "root"
  key_path: "~/.ssh/id_rsa"
  password: "redhat"

kubernetes:
  version: "1.33"
  clusters:
    - name: "cluster-1"
      pod_cidr: "10.244.0.0/16"
      service_cidr: "10.245.0.0/16"
      cni: "ovn-kubernetes"
`

	err := os.WriteFile(configPath, []byte(configContent), 0o644)
	require.NoError(t, err)

	// Test loading config
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	assert.NotNil(t, cfg)

	// Verify basic fields
	assert.Equal(t, 1, len(cfg.Networks))
	assert.Equal(t, "mgmt-network", cfg.Networks[0].Name)
	assert.Equal(t, "any", cfg.Networks[0].AttachTo) // Default value

	assert.Equal(t, 1, len(cfg.VMs))
	assert.Equal(t, "master-1", cfg.VMs[0].Name)
	assert.Equal(t, "host", cfg.VMs[0].Type)

	assert.Equal(t, "1.33", cfg.Kubernetes.Version)
	assert.Equal(t, 1, len(cfg.Kubernetes.Clusters))
	assert.Equal(t, "cluster-1", cfg.Kubernetes.Clusters[0].Name)
}

func TestGetDeploymentMode(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		expected    string
		expectError bool
	}{
		{
			name: "VM mode",
			config: Config{
				VMs: []VMConfig{{Name: "vm1"}},
			},
			expected:    VMDeploymentMode,
			expectError: false,
		},
		{
			name: "Baremetal mode",
			config: Config{
				BareMetal: []BareMetalConfig{{Name: "dh4", Type: HostType, K8sCluster: "cluster-1", K8sRole: string(ClusterRoleWorker), MgmtIP: "172.22.1.4", NodeIP: "172.22.1.4"}},
			},
			expected:    VMDeploymentMode,
			expectError: false,
		},
		{
			name: "Kind mode",
			config: Config{
				Kubernetes: KubernetesConfig{Clusters: []ClusterConfig{{Name: "k"}}},
				Kind: &KindConfig{
					Nodes: []KindNodeConfig{{Name: "cp", K8sRole: "control-plane", K8sCluster: "k"}},
				},
			},
			expected:    KindDeploymentMode,
			expectError: false,
		},
		{
			name: "Both modes - error",
			config: Config{
				VMs:        []VMConfig{{Name: "vm1"}},
				Kubernetes: KubernetesConfig{Clusters: []ClusterConfig{{Name: "k"}}},
				Kind: &KindConfig{
					Nodes: []KindNodeConfig{{Name: "cp", K8sRole: "control-plane", K8sCluster: "k"}},
				},
			},
			expected:    "",
			expectError: true,
		},
		{
			name:        "No mode - error",
			config:      Config{},
			expected:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, err := tt.config.GetDeploymentMode()
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, mode)
			}
		})
	}
}

func TestGetHostDPUMappings(t *testing.T) {
	cfg := Config{
		VMs: []VMConfig{
			{Name: "host-1", Type: HostType},
			{Name: "dpu-1a", Type: DpuType, Host: "host-1"},
			{Name: "dpu-1b", Type: DpuType, Host: "host-1"},
			{Name: "host-2", Type: HostType},
			{Name: "dpu-2", Type: DpuType, Host: "host-2"},
			{Name: "standalone", Type: HostType},
		},
	}

	mappings := cfg.GetHostDPUMappings()
	assert.Equal(t, 2, len(mappings))

	// Build a map for easier verification (order may vary due to map iteration)
	mappingsByHost := make(map[string]HostDPUMapping)
	for _, m := range mappings {
		mappingsByHost[m.Host.Name] = m
	}

	// Verify host-1 has 2 DPUs
	host1Mapping := mappingsByHost["host-1"]
	assert.Equal(t, "host-1", host1Mapping.Host.Name)
	assert.Equal(t, 2, len(host1Mapping.Connections))

	// Verify network names follow h2d-{host}-{dpu} format
	dpuNames := make(map[string]string)
	for _, conn := range host1Mapping.Connections {
		dpuNames[conn.DPU.Name] = conn.Link.NetworkName
	}
	assert.Equal(t, "h2d-host-1-dpu-1a", dpuNames["dpu-1a"])
	assert.Equal(t, "h2d-host-1-dpu-1b", dpuNames["dpu-1b"])

	// Verify host-2 has 1 DPU
	host2Mapping := mappingsByHost["host-2"]
	assert.Equal(t, "host-2", host2Mapping.Host.Name)
	assert.Equal(t, 1, len(host2Mapping.Connections))
	assert.Equal(t, "dpu-2", host2Mapping.Connections[0].DPU.Name)
	assert.Equal(t, "h2d-host-2-dpu-2", host2Mapping.Connections[0].Link.NetworkName)
}

func TestGetClusterConfig(t *testing.T) {
	cfg := Config{
		Kubernetes: KubernetesConfig{
			Clusters: []ClusterConfig{
				{Name: "cluster-1", PodCIDR: "10.244.0.0/16"},
				{Name: "cluster-2", PodCIDR: "10.245.0.0/16"},
			},
		},
	}

	// Test existing cluster
	cluster := cfg.GetClusterConfig("cluster-1")
	require.NotNil(t, cluster)
	assert.Equal(t, "cluster-1", cluster.Name)
	assert.Equal(t, "10.244.0.0/16", cluster.PodCIDR)

	// Test non-existing cluster
	cluster = cfg.GetClusterConfig("non-existent")
	assert.Nil(t, cluster)
}

func TestGetKindNodeCounts(t *testing.T) {
	cfg := Config{
		Kubernetes: KubernetesConfig{
			Clusters: []ClusterConfig{
				{Name: "cluster-a"},
				{Name: "cluster-b"},
			},
		},
		Kind: &KindConfig{
			Nodes: []KindNodeConfig{
				{Name: "cp-a", K8sRole: "control-plane", K8sCluster: "cluster-a"},
				{Name: "w-a1", K8sRole: "worker", K8sCluster: "cluster-a"},
				{Name: "w-a2", K8sRole: "worker", K8sCluster: "cluster-a"},
				{Name: "cp-b", K8sRole: "control-plane", K8sCluster: "cluster-b"},
				{Name: "w-b1", K8sRole: "worker", K8sCluster: "cluster-b"},
			},
		},
	}

	assert.Equal(t, 1, cfg.GetKindControlPlaneCount("cluster-a"))
	assert.Equal(t, 2, cfg.GetKindWorkerCount("cluster-a"))
	assert.Equal(t, 1, cfg.GetKindControlPlaneCount("cluster-b"))
	assert.Equal(t, 1, cfg.GetKindWorkerCount("cluster-b"))
}

func TestDPUHostManagementPortVFsCount(t *testing.T) {
	tests := []struct {
		name     string
		network  NetworkConfig
		expected int
	}{
		{
			name:     "defaults to two when enough simulated VFs are available",
			network:  NetworkConfig{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 16},
			expected: 2,
		},
		{
			name:     "uses configured count",
			network:  NetworkConfig{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 16, MgmtPortVFsCount: 4},
			expected: 4,
		},
		{
			name:     "falls back to one when only one resource VF is available",
			network:  NetworkConfig{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 3},
			expected: 1,
		},
		{
			name:     "falls back to zero when no resource VFs are available",
			network:  NetworkConfig{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 2},
			expected: 0,
		},
		{
			name:     "clamps default to zero when num pairs is below reserved channels",
			network:  NetworkConfig{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 1},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Networks: []NetworkConfig{tt.network}}
			require.NoError(t, cfg.validateAndSetDefaults())
			assert.Equal(t, tt.expected, cfg.DPUHostManagementPortVFsCount())
		})
	}
}

func TestValidateHostToDpuManagementPortVFsCount(t *testing.T) {
	tests := []struct {
		name    string
		network NetworkConfig
	}{
		{
			name:    "configured count exceeds available VFs",
			network: NetworkConfig{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 4, MgmtPortVFsCount: 3},
		},
		{
			name:    "configured count requires unavailable VF",
			network: NetworkConfig{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 2, MgmtPortVFsCount: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Networks: []NetworkConfig{tt.network}}
			err := cfg.validateAndSetDefaults()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "mgmt_port_vfs_count")
		})
	}
}

func TestDPUHostGatewaySubnet(t *testing.T) {
	tests := []struct {
		name     string
		network  NetworkConfig
		expected string
	}{
		{
			name:     "defaults gateway subnet",
			network:  NetworkConfig{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 16},
			expected: "172.30.0.0/24",
		},
		{
			name:     "uses configured gateway subnet",
			network:  NetworkConfig{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 16, GatewaySubnet: "172.31.0.0/24"},
			expected: "172.31.0.0/24",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Networks: []NetworkConfig{tt.network}}
			require.NoError(t, cfg.validateAndSetDefaults())
			assert.Equal(t, tt.expected, cfg.DPUHostGatewaySubnet())
		})
	}
}

func TestValidateHostToDpuGatewaySubnet(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 4, GatewaySubnet: "not-a-cidr"},
		},
	}

	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gateway_subnet")
}

func TestValidateHostToDpuGatewaySubnetCapacity(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 4, GatewaySubnet: "172.31.0.0/30"},
		},
		Kubernetes: KubernetesConfig{
			OffloadDPU: true,
			Clusters: []ClusterConfig{
				{Name: "host", CNI: CNIOVNKubernetes},
				{Name: "dpu", CNI: CNIKindnet},
			},
		},
		Kind: &KindConfig{Nodes: []KindNodeConfig{
			{Name: "host-1", Type: HostType, K8sCluster: "host", K8sRole: "worker"},
			{Name: "dpu-1", Type: DpuType, K8sCluster: "dpu", K8sRole: "worker", Host: "host-1"},
		}},
	}

	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least 3")
}

func TestKindDPUGatewayOpts(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 16, GatewaySubnet: "172.31.0.0/24"},
		},
		Kubernetes: KubernetesConfig{
			OffloadDPU: true,
			Clusters: []ClusterConfig{
				{Name: "host", CNI: CNIOVNKubernetes},
				{Name: "dpu", CNI: CNIKindnet},
			},
		},
		Kind: &KindConfig{Nodes: []KindNodeConfig{
			{Name: "host-1", Type: HostType, K8sCluster: "host", K8sRole: "worker"},
			{Name: "dpu-1", Type: DpuType, K8sCluster: "dpu", K8sRole: "worker", Host: "host-1"},
		}},
	}
	require.NoError(t, cfg.validateAndSetDefaults())

	assert.Equal(t, "--gateway-interface=eth1 --gateway-router-subnet=172.31.0.0/24 --gateway-nexthop=172.31.0.1", cfg.GatewayOpts("dpu"))
	assert.Equal(t, "--gateway-interface=eth0", cfg.GatewayOpts("host"))
}

func TestIsKindMode(t *testing.T) {
	vmConfig := Config{
		VMs: []VMConfig{{Name: "vm1"}},
	}
	assert.False(t, vmConfig.IsKindMode())
	assert.True(t, vmConfig.IsVMMode())

	kindConfig := Config{
		Kubernetes: KubernetesConfig{Clusters: []ClusterConfig{{Name: "k"}}},
		Kind: &KindConfig{
			Nodes: []KindNodeConfig{{Name: "cp", K8sRole: "control-plane", K8sCluster: "k"}},
		},
	}
	assert.True(t, kindConfig.IsKindMode())
	assert.False(t, kindConfig.IsVMMode())
}

func TestValidateOperatingSystemAllowsImageRef(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{
				Name:       "mgmt-network",
				Type:       MgmtNetworkName,
				BridgeName: "virbr-mgmt",
				Mode:       "nat",
				NICModel:   "virtio",
			},
		},
		VMs: []VMConfig{
			{
				Name:       "master-1",
				Type:       HostType,
				K8sCluster: "cluster-1",
				K8sRole:    string(ClusterRoleMaster),
				K8sNodeMAC: "52:54:00:00:01:11",
				K8sNodeIP:  "192.168.123.11",
				Memory:     4096,
				VCPUs:      2,
				DiskSize:   20,
			},
		},
		OperatingSystem: OSConfig{
			ImageRef:  "ghcr.io/example/fedora-cloud:43",
			ImageName: "Fedora-x86_64.qcow2",
		},
		Kubernetes: KubernetesConfig{
			Clusters: []ClusterConfig{
				{Name: "cluster-1", CNI: CNIOVNKubernetes},
			},
		},
	}

	err := cfg.validateAndSetDefaults()
	require.NoError(t, err)
}

func TestValidateMultusNotPrimaryCNI(t *testing.T) {
	cfg := Config{
		Kubernetes: KubernetesConfig{
			Clusters: []ClusterConfig{
				{Name: "cluster-1", CNI: "multus"},
			},
		},
	}

	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "'cni' must be"))
}

func TestValidateMultusAndPrimaryCNI(t *testing.T) {
	cfg := Config{
		Kubernetes: KubernetesConfig{
			Clusters: []ClusterConfig{
				{Name: "cluster-1", CNI: CNIFlannel, Addons: []AddonType{AddonMultus}},
			},
		},
	}

	err := cfg.validateAndSetDefaults()
	require.NoError(t, err)
	addons := cfg.GetClusterConfig("cluster-1").Addons
	require.Contains(t, addons, AddonMultus)
}

func TestValidateCertManagerAddon(t *testing.T) {
	cfg := Config{
		Kubernetes: KubernetesConfig{
			Clusters: []ClusterConfig{
				{Name: "cluster-1", CNI: CNIFlannel, Addons: []AddonType{AddonCertManager}},
			},
		},
	}

	err := cfg.validateAndSetDefaults()
	require.NoError(t, err)
	addons := cfg.GetClusterConfig("cluster-1").Addons
	require.Contains(t, addons, AddonCertManager)
}

func TestValidateWhereaboutsAddon(t *testing.T) {
	cfg := Config{
		Kubernetes: KubernetesConfig{
			Clusters: []ClusterConfig{
				{Name: "cluster-1", CNI: CNIFlannel, Addons: []AddonType{AddonWhereabouts}},
			},
		},
	}

	err := cfg.validateAndSetDefaults()
	require.NoError(t, err)
	addons := cfg.GetClusterConfig("cluster-1").Addons
	require.Contains(t, addons, AddonWhereabouts)
}

func TestValidateUnknownAddonFails(t *testing.T) {
	cfg := Config{
		Kubernetes: KubernetesConfig{
			Clusters: []ClusterConfig{
				{Name: "cluster-1", CNI: CNIFlannel, Addons: []AddonType{"unknown-addon"}},
			},
		},
	}

	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "unsupported addon"))
}

func TestValidateAddonsDeduplicatesPreservingOrder(t *testing.T) {
	cfg := Config{
		Kubernetes: KubernetesConfig{
			Clusters: []ClusterConfig{
				{
					Name:   "cluster-1",
					CNI:    CNIFlannel,
					Addons: []AddonType{AddonMultus, AddonCertManager, AddonWhereabouts, AddonMultus, AddonCertManager, AddonWhereabouts},
				},
			},
		},
	}

	err := cfg.validateAndSetDefaults()
	require.NoError(t, err)
	addons := cfg.GetClusterConfig("cluster-1").Addons
	require.Equal(t, []AddonType{AddonMultus, AddonCertManager, AddonWhereabouts}, addons)
}

func TestValidateOperatingSystemRequiresURLOrRef(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{
				Name:       "mgmt-network",
				Type:       MgmtNetworkName,
				BridgeName: "virbr-mgmt",
				Mode:       "nat",
				NICModel:   "virtio",
			},
		},
		VMs: []VMConfig{
			{
				Name:       "master-1",
				Type:       HostType,
				K8sCluster: "cluster-1",
				K8sRole:    string(ClusterRoleMaster),
				K8sNodeMAC: "52:54:00:00:01:11",
				K8sNodeIP:  "192.168.123.11",
				Memory:     4096,
				VCPUs:      2,
				DiskSize:   20,
			},
		},
		OperatingSystem: OSConfig{
			ImageName: "Fedora-x86_64.qcow2",
		},
		Kubernetes: KubernetesConfig{
			Clusters: []ClusterConfig{
				{Name: "cluster-1", CNI: CNIOVNKubernetes},
			},
		},
	}

	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "one of 'image_url' or 'image_ref' is required"))
}

func TestValidateOperatingSystemRejectsURLAndRefTogether(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{{
			Name:       "mgmt-network",
			Type:       MgmtNetworkName,
			BridgeName: "virbr-mgmt",
			Mode:       "nat",
			NICModel:   "virtio",
		}},
		VMs: []VMConfig{{
			Name:       "master-1",
			Type:       HostType,
			K8sCluster: "cluster-1",
			K8sRole:    string(ClusterRoleMaster),
			K8sNodeMAC: "52:54:00:00:01:11",
			K8sNodeIP:  "192.168.123.11",
			Memory:     4096,
			VCPUs:      2,
			DiskSize:   20,
		}},
		OperatingSystem: OSConfig{
			ImageURL:  "https://example.invalid/fedora.qcow2",
			ImageRef:  "ghcr.io/example/fedora-cloud:43",
			ImageName: "Fedora-x86_64.qcow2",
		},
		Kubernetes: KubernetesConfig{Clusters: []ClusterConfig{{Name: "cluster-1", CNI: CNIOVNKubernetes}}},
	}

	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "'image_url' and 'image_ref' are mutually exclusive"))
}

func TestValidateBareMetalConfig(t *testing.T) {
	cfg := Config{
		BareMetal: []BareMetalConfig{
			{
				Name:       "dh4",
				Type:       HostType,
				K8sCluster: "cluster-1",
				K8sRole:    string(ClusterRoleWorker),
				MgmtIP:     "172.22.1.4",
				NodeIP:     "172.22.1.4",
			},
		},
		Kubernetes: KubernetesConfig{Clusters: []ClusterConfig{{Name: "cluster-1", CNI: CNIOVNKubernetes}}},
	}

	err := cfg.validateAndSetDefaults()
	require.NoError(t, err)
}

func TestValidateBareMetalBootcSwitchRequiresImageRef(t *testing.T) {
	cfg := Config{
		BareMetal: []BareMetalConfig{
			{
				Name:       "dh4",
				Type:       HostType,
				K8sCluster: "cluster-1",
				K8sRole:    string(ClusterRoleWorker),
				MgmtIP:     "172.22.1.4",
				NodeIP:     "172.22.1.4",
				Bootc: &BareMetalBootcConfig{
					Enabled:  true,
					Strategy: "switch",
				},
			},
		},
		Kubernetes: KubernetesConfig{Clusters: []ClusterConfig{{Name: "cluster-1", CNI: CNIOVNKubernetes}}},
	}

	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bootc.image_ref is required")
}

func TestGetBareMetalClusterRoleMapping(t *testing.T) {
	cfg := Config{
		BareMetal: []BareMetalConfig{
			{Name: "dh4", Type: HostType, K8sCluster: "cluster-1", K8sRole: string(ClusterRoleWorker), MgmtIP: "172.22.1.4", NodeIP: "172.22.1.4"},
			{Name: "dh5", Type: HostType, K8sCluster: "cluster-1", K8sRole: string(ClusterRoleWorker), MgmtIP: "172.22.1.5", NodeIP: "172.22.1.5"},
		},
	}

	m := cfg.GetBareMetalClusterRoleMapping()
	require.Contains(t, m, "cluster-1")
	assert.Len(t, m["cluster-1"][ClusterRoleWorker], 2)
}

func TestRegistryEnablementModes(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name            string
		cfg             Config
		enabledExpected bool
		hasRegistry     bool
	}{
		{
			name:            "registry absent",
			cfg:             Config{},
			enabledExpected: false,
			hasRegistry:     false,
		},
		{
			name:            "registry present defaults enabled",
			cfg:             Config{Registry: &RegistryConfig{}},
			enabledExpected: true,
			hasRegistry:     false,
		},
		{
			name:            "registry explicitly disabled",
			cfg:             Config{Registry: &RegistryConfig{Enabled: &falseVal}},
			enabledExpected: false,
			hasRegistry:     false,
		},
		{
			name:            "registry explicitly enabled",
			cfg:             Config{Registry: &RegistryConfig{Enabled: &trueVal}},
			enabledExpected: true,
			hasRegistry:     false,
		},
		{
			name: "registry with container entries",
			cfg: Config{Registry: &RegistryConfig{Containers: []RegistryContainerConfig{{
				Name: "ovn-kube",
				CNI:  string(CNIOVNKubernetes),
				Tag:  "ovn-kube:dpu-sim",
			}}}},
			enabledExpected: true,
			hasRegistry:     true,
		},
		{
			name: "registry disabled with container entries",
			cfg: Config{Registry: &RegistryConfig{Enabled: &falseVal, Containers: []RegistryContainerConfig{{
				Name: "ovn-kube",
				CNI:  string(CNIOVNKubernetes),
				Tag:  "ovn-kube:dpu-sim",
			}}}},
			enabledExpected: false,
			hasRegistry:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.enabledExpected, tt.cfg.IsRegistryEnabled())
			assert.Equal(t, tt.hasRegistry, tt.cfg.HasRegistry())
		})
	}
}

func TestIsRegistryImageBuildOnly(t *testing.T) {
	falseVal := false
	ovnCont := []RegistryContainerConfig{{Name: "ovn", CNI: string(CNIOVNKubernetes), Tag: "ovn-kube:dpu-sim"}}

	assert.False(t, (&Config{}).IsRegistryImageBuildOnly())
	assert.False(t, (&Config{Registry: &RegistryConfig{Enabled: &falseVal}}).IsRegistryImageBuildOnly())
	assert.True(t, (&Config{Registry: &RegistryConfig{Enabled: &falseVal, Containers: ovnCont}}).IsRegistryImageBuildOnly())
	assert.False(t, (&Config{Registry: &RegistryConfig{Containers: ovnCont}}).IsRegistryImageBuildOnly())
}

func TestOvnKubernetesImageForHelm(t *testing.T) {
	falseVal := false
	ovnCont := []RegistryContainerConfig{{Name: "ovn", CNI: string(CNIOVNKubernetes), Tag: "ovn-kube:dpu-sim"}}
	kindCfg := &Config{
		Kubernetes: KubernetesConfig{Clusters: []ClusterConfig{{Name: "k", CNI: CNIOVNKubernetes}}},
		Kind:       &KindConfig{Nodes: []KindNodeConfig{{Name: "cp", K8sRole: "control-plane", K8sCluster: "k"}}},
	}

	t.Run("no ovn registry entry uses default", func(t *testing.T) {
		assert.Equal(t, "ghcr.io/upstream:master", kindCfg.OvnKubernetesImageForHelm("ghcr.io/upstream:master"))
	})

	t.Run("kind registry build-only uses localhost ref for kind load", func(t *testing.T) {
		cfg := *kindCfg
		cfg.Registry = &RegistryConfig{Enabled: &falseVal, Containers: ovnCont}
		assert.Equal(t, "localhost/ovn-kube:dpu-sim", (&cfg).OvnKubernetesImageForHelm("ghcr.io/upstream:master"))
	})

	t.Run("registry enabled uses node endpoint ref", func(t *testing.T) {
		cfg := *kindCfg
		cfg.Registry = &RegistryConfig{Containers: ovnCont}
		assert.Equal(t, "localhost:5000/ovn-kube:dpu-sim", (&cfg).OvnKubernetesImageForHelm("ghcr.io/upstream:master"))
	})
}

func TestKindNodeLocalImageRef(t *testing.T) {
	assert.Equal(t, "", KindNodeLocalImageRef(""))
	assert.Equal(t, "localhost/ovn-kube:dpu-sim", KindNodeLocalImageRef("ovn-kube:dpu-sim"))
	assert.Equal(t, "localhost/foo/bar:latest", KindNodeLocalImageRef("foo/bar:latest"))
	assert.Equal(t, "localhost/ovn-kube:dpu-sim", KindNodeLocalImageRef("localhost/ovn-kube:dpu-sim"))
	assert.Equal(t, "ghcr.io/ovn-org/ovn-kube:tag", KindNodeLocalImageRef("ghcr.io/ovn-org/ovn-kube:tag"))
	assert.Equal(t, "registry:5000/ns/img:v1", KindNodeLocalImageRef("registry:5000/ns/img:v1"))
}

func TestClusterNeedsOVNKubernetesImage(t *testing.T) {
	host := ClusterConfig{Name: "host", CNI: CNIOVNKubernetes}
	dpu := ClusterConfig{Name: "dpu", CNI: CNIFlannel}
	cfg := &Config{
		Kubernetes: KubernetesConfig{OffloadDPU: true, Clusters: []ClusterConfig{host, dpu}},
		Kind: &KindConfig{Nodes: []KindNodeConfig{
			{Name: "h", Type: HostType, K8sRole: "worker", K8sCluster: "host"},
			{Name: "d", Type: DpuType, K8sRole: "worker", K8sCluster: "dpu", Host: "h"},
		}},
	}
	assert.True(t, cfg.ClusterNeedsOVNKubernetesImage("host"))
	assert.True(t, cfg.ClusterNeedsOVNKubernetesImage("dpu"))
	assert.False(t, (&Config{
		Kubernetes: KubernetesConfig{Clusters: []ClusterConfig{{Name: "x", CNI: CNIFlannel}}},
	}).ClusterNeedsOVNKubernetesImage("x"))
}

func TestGetRegistryInsecureEndpoints(t *testing.T) {
	t.Run("returns empty when registry is not enabled", func(t *testing.T) {
		cfg := Config{
			Networks: []NetworkConfig{{
				Name:       "mgmt-network",
				Type:       MgmtNetworkName,
				BridgeName: "virbr-mgmt",
				Gateway:    "192.168.120.1",
				Mode:       "nat",
				NICModel:   "virtio",
			}},
			BareMetal: []BareMetalConfig{{
				Name:       "dh4",
				Type:       HostType,
				K8sCluster: "cluster-1",
				K8sRole:    string(ClusterRoleWorker),
				MgmtIP:     "172.22.1.4",
				NodeIP:     "172.22.1.4",
			}},
			Kubernetes: KubernetesConfig{Clusters: []ClusterConfig{{Name: "cluster-1", CNI: CNIFlannel}}},
		}

		err := cfg.validateAndSetDefaults()
		require.NoError(t, err)
		require.Empty(t, cfg.GetRegistryInsecureEndpoints())
	})

	t.Run("fallback to vm registry node endpoint when registry is enabled", func(t *testing.T) {
		cfg := Config{
			Registry: &RegistryConfig{},
			Networks: []NetworkConfig{{
				Name:       "mgmt-network",
				Type:       MgmtNetworkName,
				BridgeName: "virbr-mgmt",
				Gateway:    "192.168.120.1",
				Mode:       "nat",
				NICModel:   "virtio",
			}},
			BareMetal: []BareMetalConfig{{
				Name:       "dh4",
				Type:       HostType,
				K8sCluster: "cluster-1",
				K8sRole:    string(ClusterRoleWorker),
				MgmtIP:     "172.22.1.4",
				NodeIP:     "172.22.1.4",
			}},
			Kubernetes: KubernetesConfig{Clusters: []ClusterConfig{{Name: "cluster-1", CNI: CNIFlannel}}},
		}

		err := cfg.validateAndSetDefaults()
		require.NoError(t, err)
		require.Equal(t, []string{"192.168.120.1:5000"}, cfg.GetRegistryInsecureEndpoints())
	})

	t.Run("uses configured insecure endpoints", func(t *testing.T) {
		cfg := Config{
			Registry: &RegistryConfig{
				InsecureEndpoints: []string{"172.22.1.100:5000", " 192.168.120.1:5000 ", "172.22.1.100:5000"},
			},
			Kubernetes: KubernetesConfig{Clusters: []ClusterConfig{{Name: "cluster-1", CNI: CNIFlannel}}},
		}

		err := cfg.validateAndSetDefaults()
		require.NoError(t, err)
		require.Equal(t, []string{"172.22.1.100:5000", "192.168.120.1:5000"}, cfg.GetRegistryInsecureEndpoints())
	})
}

func TestValidateRegistryInsecureEndpoints(t *testing.T) {
	cfg := Config{
		Registry: &RegistryConfig{
			InsecureEndpoints: []string{"http://172.22.1.100:5000"},
		},
		Kubernetes: KubernetesConfig{Clusters: []ClusterConfig{{Name: "cluster-1", CNI: CNIFlannel}}},
	}

	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registry.insecure_endpoints[0]")
}

func TestLoadConfigKindWithTrafficFlowFields(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `
networks:
  - name: "host-to-dpu-link"
    type: "HostToDpu"
    num_pairs: 1
kind:
  nodes:
    - name: "cp"
      k8s_role: "control-plane"
      k8s_cluster: "dpu-sim-host"
kubernetes:
  version: "1.33"
  clusters:
    - name: "dpu-sim-host"
      cni: "ovn-kubernetes"
operating_system:
  image_name: "unused-in-kind"
ssh:
  user: "root"
  key_path: "/tmp/dpu-sim-test-key"
tft:
  - name: "Test 1"
    namespace: "default"
    test_cases: "1"
    duration: "5"
    connections: []
kubeconfig: "kc/host.kubeconfig"
`
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o644))
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg.TFT)
	require.NotNil(t, cfg.TFT.Node())
	assert.Equal(t, yaml.SequenceNode, cfg.TFT.Node().Kind)
	assert.Equal(t, "kc/host.kubeconfig", cfg.TrafficFlowTestsKubeconfig)
}
