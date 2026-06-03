package config

const (
	// DefaultMgmtPortVFsCount is the number of simulated management-port VFs
	// (eth0-1..eth0-N) when networks[].mgmt_port_vfs_count is unset.
	DefaultMgmtPortVFsCount = 2

	// DefaultRegistryContainerName is the Docker container name for the local registry.
	DefaultRegistryContainerName = "dpu-sim-registry"
	// DefaultRegistryPort is the host port the registry listens on.
	DefaultRegistryPort = "5000"
	// DefaultRegistryImage is the Docker image used for the registry.
	DefaultRegistryImage = "registry:2"

	defaultDPUHostGatewaySubnet    = "172.30.0.0/24"
	defaultKindDPUGatewayNetwork   = "dpu-sim-gateway"
	defaultKindDPUGatewayInterface = "eth1"
)
