package config

import (
	"fmt"
	"net"

	"gopkg.in/yaml.v3"
)

const (
	MgmtNetworkName      = "mgmt"
	K8sNetworkName       = "k8s"
	KindK8sNetworkName   = "eth0"
	HostToDpuNetworkType = "HostToDpu"
	VMDeploymentMode     = "vm"
	KindDeploymentMode   = "kind"
	HostType             = "host"
	DpuType              = "dpu"

	// RegistryContainerName is the Docker container name for the local registry
	DefaultRegistryContainerName = "dpu-sim-registry"
	// RegistryPort is the host port the registry listens on
	DefaultRegistryPort = "5000"
	// RegistryImage is the Docker image used for the registry
	DefaultRegistryImage = "registry:2"

	// OVNKubernetesModeInstall deploys OVN-Kubernetes with Helm as part of dpu-sim.
	OVNKubernetesModeInstall = "install"
	// OVNKubernetesModeValuesOnly generates Helm values and support artifacts but
	// leaves the OVN-Kubernetes Helm deployment to the caller.
	OVNKubernetesModeValuesOnly = "values-only"
)

// DPUHostNodeLabelKey is the node label used by OVN-Kubernetes for DPU Host Nodes
const DPUHostNodeLabelKey = "k8s.ovn.org/dpu-host"

// CNIType represents the type of CNI
type CNIType string

const (
	CNIFlannel       CNIType = "flannel"
	CNIOVNKubernetes CNIType = "ovn-kubernetes"
	CNIKindnet       CNIType = "kindnet"
)

type AddonType string

const (
	AddonMultus      AddonType = "multus"
	AddonCertManager AddonType = "cert-manager"
	AddonWhereabouts AddonType = "whereabouts"
)

// Config represents the complete DPU simulator configuration
type Config struct {
	Networks        []NetworkConfig   `yaml:"networks"`
	VMs             []VMConfig        `yaml:"vms"`
	BareMetal       []BareMetalConfig `yaml:"baremetal,omitempty"`
	Kind            *KindConfig       `yaml:"kind,omitempty"`
	OperatingSystem OSConfig          `yaml:"operating_system"`
	SSH             SSHConfig         `yaml:"ssh"`
	Kubernetes      KubernetesConfig  `yaml:"kubernetes"`
	Registry        *RegistryConfig   `yaml:"registry,omitempty"`
	// OVNKubernetesPath overrides the default ovn-kubernetes source location
	// (auto-cloned). Set via --ovn-kubernetes-path so external CI can
	// point dpu-sim at a separate checkout (e.g. an OVN-Kubernetes PR).
	// This is not populated from YAML.
	OVNKubernetesPath string `yaml:"-"`
	// OVNKubernetesMode controls whether dpu-sim installs OVN-Kubernetes or only
	// emits values files for an external Helm install. Set via --ovnk-mode.
	// This is not populated from YAML.
	OVNKubernetesMode string `yaml:"-"`
	// TFT is the kubernetes-traffic-flow-tests "tft" document subtree (optional).
	// Used by `dpu-sim tft run` to generate a TFT config when --tft-config is not set.
	TFT *TrafficFlowTestsSubtree `yaml:"tft,omitempty"`
	// TrafficFlowTestsKubeconfig is the kubeconfig path for TFT (yaml key "kubeconfig").
	// When empty, tft run defaults to the first kubernetes.clusters entry (Kind or VM).
	TrafficFlowTestsKubeconfig string `yaml:"kubeconfig,omitempty"`
}

// TrafficFlowTestsSubtree holds the raw tft: YAML value (sequence or mapping) for the TFT harness.
type TrafficFlowTestsSubtree struct {
	n yaml.Node
}

func (t *TrafficFlowTestsSubtree) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	t.n = *value
	return nil
}

// Node returns the decoded tft subtree, or nil if unset.
func (t *TrafficFlowTestsSubtree) Node() *yaml.Node {
	if t == nil || t.n.Kind == 0 {
		return nil
	}
	return &t.n
}

// RegistryConfig represents the local container registry configuration.
// With registry.enabled true (default when omitted), dpu-sim starts a local
// registry, builds configured images, and pushes them for cluster pulls.
//
// With registry.enabled false in Kind mode, dpu-sim still builds those images
// but loads them into Kind nodes ("kind load") instead of using a registry.
type RegistryConfig struct {
	// Enabled controls whether dpu-sim should manage the local registry.
	// Defaults to true when the registry section is present.
	Enabled *bool `yaml:"enabled,omitempty"`
	// InsecureEndpoints is the list of registry endpoints (host:port)
	// that nodes should treat as insecure HTTP registries.
	InsecureEndpoints []string                  `yaml:"insecure_endpoints,omitempty"`
	Containers        []RegistryContainerConfig `yaml:"containers"`
}

// RegistryContainerConfig represents a container image to build and push
// to the local registry.
type RegistryContainerConfig struct {
	// Name is a human-readable identifier for this container build
	Name string `yaml:"name"`
	// CNI is the CNI type whose source will be compiled (e.g. "ovn-kubernetes")
	CNI string `yaml:"cni"`
	// Tag is the image name:tag to use when pushing to the local registry
	// (e.g. "ovn-kube:dpu-sim")
	Tag string `yaml:"tag"`
}

// NetworkConfig represents a network configuration
type NetworkConfig struct {
	Name       string `yaml:"name"`
	Type       string `yaml:"type"`
	BridgeName string `yaml:"bridge_name"`
	Gateway    string `yaml:"gateway,omitempty"`
	SubnetMask string `yaml:"subnet_mask,omitempty"`
	DHCPStart  string `yaml:"dhcp_start,omitempty"`
	DHCPEnd    string `yaml:"dhcp_end,omitempty"`
	Mode       string `yaml:"mode"`
	NICModel   string `yaml:"nic_model"`
	UseOVS     bool   `yaml:"use_ovs,omitempty"`
	AttachTo   string `yaml:"attach_to,omitempty"`
	NumPairs   int    `yaml:"num_pairs,omitempty"`
	// GatewaySubnet is the subnet used by simulated DPU gateway interfaces.
	// It only applies to HostToDpu networks.
	GatewaySubnet string `yaml:"gateway_subnet,omitempty"`
	// MgmtPortVFsCount is the number of simulated VFs requested by
	// ovnkube-node for default and primary UDN management ports. It only
	// applies to HostToDpu networks.
	MgmtPortVFsCount int `yaml:"mgmt_port_vfs_count,omitempty"`
}

// BareMetalConfig represents a bare metal configuration
type BareMetalConfig struct {
	Name             string                `yaml:"name"`
	Type             string                `yaml:"type,omitempty"`
	K8sCluster       string                `yaml:"k8s_cluster,omitempty"`
	K8sRole          string                `yaml:"k8s_role,omitempty"`
	MgmtIP           string                `yaml:"mgmt_ip,omitempty"`
	NodeIP           string                `yaml:"node_ip,omitempty"`
	Host             string                `yaml:"host,omitempty"`
	GatewayInterface string                `yaml:"gateway_interface,omitempty"`
	ProtectedIfaces  []string              `yaml:"protected_interfaces,omitempty"`
	BootstrapSSH     *SSHConfig            `yaml:"bootstrap_ssh,omitempty"`
	Bootc            *BareMetalBootcConfig `yaml:"bootc,omitempty"`
}

// BareMetalBootcConfig controls optional bootc reconciliation for adopted nodes.
type BareMetalBootcConfig struct {
	Enabled                   bool   `yaml:"enabled,omitempty"`
	Strategy                  string `yaml:"strategy,omitempty"`
	ImageRef                  string `yaml:"image_ref,omitempty"`
	Transport                 string `yaml:"transport,omitempty"`
	Apply                     bool   `yaml:"apply,omitempty"`
	SoftReboot                string `yaml:"soft_reboot,omitempty"`
	Retain                    bool   `yaml:"retain,omitempty"`
	EnforceContainerSigpolicy bool   `yaml:"enforce_container_sigpolicy,omitempty"`
	WaitAfterReboot           string `yaml:"wait_after_reboot,omitempty"`
}

// VMConfig represents a virtual machine configuration
type VMConfig struct {
	Name       string `yaml:"name"`
	Type       string `yaml:"type"`
	K8sCluster string `yaml:"k8s_cluster,omitempty"`
	K8sRole    string `yaml:"k8s_role,omitempty"`
	K8sNodeMAC string `yaml:"k8s_node_mac,omitempty"`
	K8sNodeIP  string `yaml:"k8s_node_ip,omitempty"`
	Host       string `yaml:"host,omitempty"`
	Memory     int    `yaml:"memory"`
	VCPUs      int    `yaml:"vcpus"`
	DiskSize   int    `yaml:"disk_size"`
}

// KindConfig represents Kind cluster configuration
type KindConfig struct {
	Nodes []KindNodeConfig `yaml:"nodes"`
}

// KindNodeConfig represents a Kind node configuration.
type KindNodeConfig struct {
	Name       string `yaml:"name"`                  // Name is used as a node label (dpu-sim.org/node-name) since Kind does not support node renaming.
	Type       string `yaml:"type,omitempty"`        // "host" or "dpu" for workers
	K8sCluster string `yaml:"k8s_cluster,omitempty"` // Kubernetes cluster name
	K8sRole    string `yaml:"k8s_role,omitempty"`    // "control-plane" or "worker"
	Host       string `yaml:"host,omitempty"`        // for type "dpu", the name of the host node
}

// OSConfig represents operating system configuration
type OSConfig struct {
	ImageURL  string `yaml:"image_url,omitempty"`
	ImageRef  string `yaml:"image_ref,omitempty"`
	ImageName string `yaml:"image_name"`
}

// SSHConfig represents SSH configuration
type SSHConfig struct {
	User     string `yaml:"user"`
	KeyPath  string `yaml:"key_path"`
	Password string `yaml:"password"`
}

// KubernetesConfig represents Kubernetes configuration
type KubernetesConfig struct {
	Version       string          `yaml:"version"`
	KubeconfigDir string          `yaml:"kubeconfig_dir,omitempty"`
	OffloadDPU    bool            `yaml:"offload_dpu,omitempty"`
	Clusters      []ClusterConfig `yaml:"clusters"`
}

// GetKubeconfigDir returns the kubeconfig directory, defaulting to "kubeconfig" if not set
func (k *KubernetesConfig) GetKubeconfigDir() string {
	if k.KubeconfigDir == "" {
		return "kubeconfig"
	}
	return k.KubeconfigDir
}

// ClusterConfig represents a Kubernetes cluster configuration
type ClusterConfig struct {
	Name        string      `yaml:"name"`
	PodCIDR     string      `yaml:"pod_cidr"`
	ServiceCIDR string      `yaml:"service_cidr"`
	CNI         CNIType     `yaml:"cni"`
	Addons      []AddonType `yaml:"addons"`
}

// HostDPULink represents network link information between a host and DPU
type HostDPULink struct {
	NetworkName string // Network name in format "h2d-{host_name}-{dpu_name}"
}

// DPUConnection represents a DPU and its link to the host
type DPUConnection struct {
	DPU  VMConfig
	Link HostDPULink
}

// HostDPUMapping represents a host and all its connected DPUs
type HostDPUMapping struct {
	Host        VMConfig
	Connections []DPUConnection
}

type ClusterRole string

const (
	ClusterRoleMaster ClusterRole = "master"
	ClusterRoleWorker ClusterRole = "worker"
)

// ClusterRoleMapping maps roles (master/worker) to their VM configurations
type ClusterRoleMapping map[ClusterRole][]VMConfig

// BareMetalClusterRoleMapping maps roles (master/worker) to baremetal configurations.
type BareMetalClusterRoleMapping map[ClusterRole][]BareMetalConfig

// GetSubnetCIDR returns the subnet in CIDR notation (e.g., "192.168.120.0/24")
// derived from the gateway and subnet mask
func (n *NetworkConfig) GetSubnetCIDR() string {
	if n.Gateway == "" || n.SubnetMask == "" {
		return ""
	}

	gwIP := net.ParseIP(n.Gateway)
	maskIP := net.ParseIP(n.SubnetMask)
	if gwIP == nil || maskIP == nil {
		return ""
	}

	mask := net.IPMask(maskIP.To4())
	networkAddr := gwIP.Mask(mask)
	ones, _ := mask.Size()

	return fmt.Sprintf("%s/%d", networkAddr, ones)
}
