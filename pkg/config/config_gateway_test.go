package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVMGatewayUsesActualLibvirtNetwork(t *testing.T) {
	cfg, err := LoadConfig("../../config-ovnk-offload.yaml")
	require.NoError(t, err)
	// Use a non-default gateway to catch code that assumes the first address.
	cfg.GetNetworkByType(K8sNetworkName).Gateway = "192.168.123.9"
	require.Equal(t, "172.30.0.0/24", cfg.DPUHostGatewaySubnet())
	require.Equal(t, "dpu-sim-gateway", cfg.DPUGatewayNetworkName())
	require.Equal(t, "dpu-sim-gw", cfg.DPUGatewayBridgeName())
	require.Equal(t, "--gateway-interface=gateway --gateway-router-subnet=172.30.0.0/24 --gateway-nexthop=172.30.0.1", cfg.GatewayOpts("dpu-sim-dpu"))
	require.Equal(t, "--gateway-interface=k8s", cfg.GatewayOpts("dpu-sim-host"))
	gw := cfg.GetGatewayNetwork()
	require.NotNil(t, gw)
	require.Equal(t, GatewayNetworkName, gw.Type)
	require.Equal(t, DpuType, gw.AttachTo)
}

func TestVMGatewayAddressPlan(t *testing.T) {
	cfg, err := LoadConfig("../../config-ovnk-offload.yaml")
	require.NoError(t, err)
	for node, want := range map[string]string{"dpu-1-1": "172.30.0.2", "dpu-2-1": "172.30.0.3", "host-1-1": "172.30.0.254", "host-2-1": "172.30.0.253"} {
		ip, err := cfg.VMGatewayIP(node)
		require.NoError(t, err)
		require.Equal(t, want, ip)
	}
	_, err = cfg.VMGatewayIP("master-1")
	require.Error(t, err)

	gw := cfg.GetGatewayNetwork()
	require.NotNil(t, gw)
	gw.Gateway = "10.100.0.1"
	gw.SubnetMask = "255.255.255.248"
	require.NoError(t, cfg.validateVMGateway())
	ip, err := cfg.VMGatewayIP("host-2-1")
	require.NoError(t, err)
	require.Equal(t, "10.100.0.5", ip)

	gw.Gateway = "10.100.0.1"
	gw.SubnetMask = "255.255.255.252"
	require.Error(t, cfg.validateVMGateway())

	gw.Gateway = "192.168.123.1"
	gw.SubnetMask = "255.255.255.0"
	require.ErrorContains(t, cfg.validateVMGateway(), "overlaps")
}

func TestGatewayNetworkSynthesizedFromHostToDpu(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{Name: "k8s", Type: K8sNetworkName, BridgeName: "ovn", Gateway: "192.168.123.1", SubnetMask: "255.255.255.0"},
			{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 4, GatewaySubnet: "172.31.0.0/24"},
		},
		Kubernetes: KubernetesConfig{
			OffloadDPU: true,
			Clusters: []ClusterConfig{
				{Name: "host", CNI: CNIOVNKubernetes},
				{Name: "dpu", CNI: CNIFlannel},
			},
		},
		VMs: []VMConfig{
			{Name: "host-1", Type: HostType, K8sCluster: "host", K8sRole: "worker", K8sNodeMAC: "52:54:00:00:00:01", K8sNodeIP: "192.168.123.11", Memory: 1024, VCPUs: 1, DiskSize: 10},
			{Name: "dpu-1", Type: DpuType, K8sCluster: "dpu", K8sRole: "worker", K8sNodeMAC: "52:54:00:00:00:02", K8sNodeIP: "192.168.123.12", Host: "host-1", Memory: 1024, VCPUs: 1, DiskSize: 10},
		},
		OperatingSystem: OSConfig{ImageURL: "http://example.com/img.qcow2", ImageName: "img.qcow2"},
		SSH:             SSHConfig{User: "root", KeyPath: "/tmp/id_rsa"},
	}
	require.NoError(t, cfg.validateAndSetDefaults())
	gw := cfg.GetGatewayNetwork()
	require.NotNil(t, gw)
	require.Equal(t, "172.31.0.0/24", cfg.DPUHostGatewaySubnet())
	require.Equal(t, "172.31.0.1", cfg.DPUGatewayNextHop())
	require.Equal(t, defaultDPUGatewayNetwork, gw.Name)
	require.Equal(t, DefaultGatewayBridge, gw.BridgeName)
	require.Equal(t, DpuType, gw.AttachTo)
	require.Empty(t, cfg.GetHostToDpuNetwork().GatewaySubnet)
}

func TestExplicitGatewayPrefersOverHostToDpuSubnet(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{
				Name: "dpu-sim-gateway", Type: GatewayNetworkName, BridgeName: DefaultGatewayBridge,
				Gateway: "172.30.0.1", SubnetMask: "255.255.255.0",
				Mode: "nat", AttachTo: DpuType,
			},
			{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 4, GatewaySubnet: "172.31.0.0/24"},
			{Name: "k8s", Type: K8sNetworkName, BridgeName: "ovn", Gateway: "192.168.123.1", SubnetMask: "255.255.255.0"},
		},
		Kubernetes: KubernetesConfig{
			OffloadDPU: true,
			Clusters: []ClusterConfig{
				{Name: "host", CNI: CNIOVNKubernetes},
				{Name: "dpu", CNI: CNIFlannel},
			},
		},
		VMs: []VMConfig{
			{Name: "host-1", Type: HostType, K8sCluster: "host", K8sRole: "worker", K8sNodeMAC: "52:54:00:00:00:01", K8sNodeIP: "192.168.123.11", Memory: 1024, VCPUs: 1, DiskSize: 10},
			{Name: "dpu-1", Type: DpuType, K8sCluster: "dpu", K8sRole: "worker", K8sNodeMAC: "52:54:00:00:00:02", K8sNodeIP: "192.168.123.12", Host: "host-1", Memory: 1024, VCPUs: 1, DiskSize: 10},
		},
		OperatingSystem: OSConfig{ImageURL: "http://example.com/img.qcow2", ImageName: "img.qcow2"},
		SSH:             SSHConfig{User: "root", KeyPath: "/tmp/id_rsa"},
	}
	require.NoError(t, cfg.validateAndSetDefaults())
	require.Equal(t, "172.30.0.0/24", cfg.DPUHostGatewaySubnet())
	require.Equal(t, "172.30.0.1", cfg.DPUGatewayNextHop())
	require.Equal(t, "172.31.0.0/24", cfg.GetHostToDpuNetwork().GatewaySubnet)
}

func TestKindAcceptsLeanGatewayNetwork(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{
				Name: "dpu-sim-gateway", Type: GatewayNetworkName,
				Gateway: "172.30.0.1", SubnetMask: "255.255.255.0",
			},
			{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 4},
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
	require.Equal(t, "172.30.0.0/24", cfg.DPUHostGatewaySubnet())
	require.Equal(t, "dpu-sim-gateway", cfg.DPUGatewayNetworkName())
	require.Equal(t, "172.30.0.1", cfg.DPUGatewayNextHop())
	require.Empty(t, cfg.GetHostToDpuNetwork().GatewaySubnet)
}

func TestKindLegacyHostToDpuGatewaySubnet(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 4, GatewaySubnet: "172.31.0.0/24"},
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
	gw := cfg.GetGatewayNetwork()
	require.NotNil(t, gw)
	require.Equal(t, "172.31.0.0/24", cfg.DPUHostGatewaySubnet())
	require.Equal(t, "172.31.0.1", cfg.DPUGatewayNextHop())
	require.Equal(t, defaultDPUGatewayNetwork, gw.Name)
	require.Empty(t, cfg.GetHostToDpuNetwork().GatewaySubnet)
}

func TestGatewayNetworkDerivesSubnet(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{
				Name: "dpu-sim-gateway", Type: GatewayNetworkName, BridgeName: DefaultGatewayBridge,
				Gateway: "172.30.0.1", SubnetMask: "255.255.255.0",
				Mode: "nat", AttachTo: DpuType,
			},
			{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 4},
			{Name: "k8s", Type: K8sNetworkName, BridgeName: "ovn", Gateway: "192.168.123.1", SubnetMask: "255.255.255.0"},
		},
		Kubernetes: KubernetesConfig{
			OffloadDPU: true,
			Clusters: []ClusterConfig{
				{Name: "host", CNI: CNIOVNKubernetes},
				{Name: "dpu", CNI: CNIFlannel},
			},
		},
		VMs: []VMConfig{
			{Name: "host-1", Type: HostType, K8sCluster: "host", K8sRole: "worker", K8sNodeMAC: "52:54:00:00:00:01", K8sNodeIP: "192.168.123.11", Memory: 1024, VCPUs: 1, DiskSize: 10},
			{Name: "dpu-1", Type: DpuType, K8sCluster: "dpu", K8sRole: "worker", K8sNodeMAC: "52:54:00:00:00:02", K8sNodeIP: "192.168.123.12", Host: "host-1", Memory: 1024, VCPUs: 1, DiskSize: 10},
		},
		OperatingSystem: OSConfig{ImageURL: "http://example.com/img.qcow2", ImageName: "img.qcow2"},
		SSH:             SSHConfig{User: "root", KeyPath: "/tmp/id_rsa"},
	}
	require.NoError(t, cfg.validateAndSetDefaults())
	gw := cfg.GetGatewayNetwork()
	require.NotNil(t, gw)
	require.Empty(t, gw.GatewaySubnet)
	require.Equal(t, "172.30.0.0/24", gw.GetSubnetCIDR())
	require.Equal(t, "172.30.0.0/24", cfg.DPUHostGatewaySubnet())
}

func TestGatewayNetworkRejectsGatewaySubnetField(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{
				Name: "dpu-sim-gateway", Type: GatewayNetworkName, BridgeName: DefaultGatewayBridge,
				Gateway: "172.30.0.1", SubnetMask: "255.255.255.0", GatewaySubnet: "172.30.0.0/24",
				Mode: "nat", AttachTo: DpuType,
			},
			{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 4},
		},
		Kubernetes: KubernetesConfig{OffloadDPU: true},
	}
	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	require.Contains(t, err.Error(), "'gateway_subnet' is not allowed")
}

func TestExplicitGatewayRejectsWrongNextHop(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{
				Name: "dpu-sim-gateway", Type: GatewayNetworkName, BridgeName: DefaultGatewayBridge,
				Gateway: "172.30.0.9", SubnetMask: "255.255.255.0",
				Mode: "nat", AttachTo: DpuType,
			},
			{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 4},
		},
		Kubernetes: KubernetesConfig{OffloadDPU: true},
	}
	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	require.Contains(t, err.Error(), "first usable address")
}

func TestRejectsMultipleGatewayNetworks(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{
				Name: "gw-a", Type: GatewayNetworkName,
				Gateway: "172.30.0.1", SubnetMask: "255.255.255.0",
			},
			{
				Name: "gw-b", Type: GatewayNetworkName,
				Gateway: "172.31.0.1", SubnetMask: "255.255.255.0",
			},
			{Name: "host-to-dpu", Type: HostToDpuNetworkType, NumPairs: 4},
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
	require.Contains(t, err.Error(), "only one network with type 'gateway' is allowed")
}

func TestRejectsMultipleHostToDpuNetworks(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{Name: "host-to-dpu-a", Type: HostToDpuNetworkType, NumPairs: 4},
			{Name: "host-to-dpu-b", Type: HostToDpuNetworkType, NumPairs: 4},
		},
	}
	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	require.Contains(t, err.Error(), "only one network with type 'HostToDpu' is allowed")
}

func TestRejectsMultipleMgmtNetworks(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{Name: "mgmt-a", Type: MgmtNetworkName, BridgeName: "virbr-a", Mode: "nat", AttachTo: "any"},
			{Name: "mgmt-b", Type: MgmtNetworkName, BridgeName: "virbr-b", Mode: "nat", AttachTo: "any"},
		},
	}
	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	require.Contains(t, err.Error(), "only one network with type 'mgmt' is allowed")
}

func TestRejectsMultipleK8sNetworks(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{Name: "k8s-a", Type: K8sNetworkName, BridgeName: "ovn-a", Mode: "nat", AttachTo: "any"},
			{Name: "k8s-b", Type: K8sNetworkName, BridgeName: "ovn-b", Mode: "nat", AttachTo: "any"},
		},
	}
	err := cfg.validateAndSetDefaults()
	require.Error(t, err)
	require.Contains(t, err.Error(), "only one network with type 'k8s' is allowed")
}
