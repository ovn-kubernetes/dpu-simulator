package config

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/netutil"
)

// DefaultGatewayBridge is the default host bridge for type: gateway networks
// in VM mode. Kind ignores bridge_name.
const DefaultGatewayBridge = "dpu-sim-gw"

// VMGatewayIP assigns DPUs from the low end and host VFs from the high end.
// Ordering follows GetHostDPUPairs, also used by the FRR routing helper.
func (c *Config) VMGatewayIP(node string) (string, error) {
	_, subnet, err := net.ParseCIDR(c.DPUHostGatewaySubnet())
	if err != nil || subnet.IP.To4() == nil {
		return "", fmt.Errorf("invalid IPv4 gateway subnet")
	}
	base := binary.BigEndian.Uint32(subnet.IP.To4())
	mask := binary.BigEndian.Uint32(subnet.Mask)
	pairs := c.GetHostDPUPairs("")
	if err := validateIPv4CIDRCapacity(subnet.String(), 1+2*len(pairs)); err != nil {
		return "", err
	}
	for i, pair := range pairs {
		var value uint32
		switch node {
		case pair.DPUNode:
			value = base + 2 + uint32(i)
		case pair.HostNode:
			value = (base | ^mask) - 1 - uint32(i)
		default:
			continue
		}
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, value)
		return ip.String(), nil
	}
	return "", fmt.Errorf("node %s has no host/DPU gateway pair", node)
}

// GetGatewayNetwork returns the dedicated DPU gateway network, or nil.
func (c *Config) GetGatewayNetwork() *NetworkConfig {
	return c.GetNetworkByType(GatewayNetworkName)
}

// synthesizeGatewayNetworkFromLegacySubnet synthesizes a type: gateway network from
// HostToDpu.gateway_subnet (or the default subnet) when YAML omitted an explicit
// gateway entry. Shared by Kind and VM so both backends resolve to a single gateway
// NetworkConfig. BridgeName/Mode/NICModel/AttachTo are set for
// VM libvirt; Kind ignores those fields and uses gateway + subnet_mask only.
func (c *Config) synthesizeGatewayNetworkFromLegacySubnet() error {
	if c.GetGatewayNetwork() != nil {
		return nil
	}
	h2d := c.GetHostToDpuNetwork()
	if h2d == nil {
		return fmt.Errorf("offload_dpu requires a HostToDpu network or an explicit gateway network")
	}
	subnet := h2d.GatewaySubnet
	if subnet == "" {
		subnet = defaultDPUHostGatewaySubnet
	}
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil || ipNet.IP.To4() == nil {
		return fmt.Errorf("invalid IPv4 gateway_subnet %q", subnet)
	}
	nextHop, err := netutil.GetFirstUsableIPv4AddressInSubnet(ipNet)
	if err != nil {
		return fmt.Errorf("gateway_subnet %q: %w", subnet, err)
	}
	// Clear legacy HostToDpu.gateway_subnet before append so the slice
	// reallocation cannot leave a stale dual source of truth.
	h2d.GatewaySubnet = ""
	c.Networks = append(c.Networks, NetworkConfig{
		Name:       defaultDPUGatewayNetwork,
		Type:       GatewayNetworkName,
		BridgeName: DefaultGatewayBridge,
		Gateway:    nextHop.String(),
		SubnetMask: net.IP(ipNet.Mask).String(),
		Mode:       "nat",
		NICModel:   "virtio",
		AttachTo:   DpuType,
	})
	return nil
}

// validateGatewayNetwork checks a type: gateway NetworkConfig: required
// gateway/subnet_mask, first-usable gateway address, capacity for host/DPU
// pairs, and rejects DHCP/OVS/VF fields. In VM mode also requires bridge_name,
// mode nat, and attach_to dpu.
func (c *Config) validateGatewayNetwork(idx int, n NetworkConfig) error {
	prefix := fmt.Sprintf("networks[%d] (%s)", idx, n.Name)
	if n.GatewaySubnet != "" {
		return fmt.Errorf("%s: 'gateway_subnet' is not allowed for type %s; derived from gateway/subnet_mask",
			prefix, GatewayNetworkName)
	}
	if n.Gateway == "" {
		return fmt.Errorf("%s: 'gateway' is required", prefix)
	}
	if n.SubnetMask == "" {
		return fmt.Errorf("%s: 'subnet_mask' is required", prefix)
	}
	derived := n.GetSubnetCIDR()
	if derived == "" {
		return fmt.Errorf("%s: 'gateway' and 'subnet_mask' must form a valid IPv4 subnet", prefix)
	}

	if err := validateIPv4CIDRCapacity(derived, c.dpuGatewaySubnetRequiredIPs()); err != nil {
		return fmt.Errorf("%s: gateway subnet must be a valid CIDR: %v", prefix, err)
	}
	_, ipNet, err := net.ParseCIDR(derived)
	if err != nil {
		return fmt.Errorf("%s: gateway subnet must be a valid CIDR: %v", prefix, err)
	}
	wantGateway, err := netutil.GetFirstUsableIPv4AddressInSubnet(ipNet)
	if err != nil {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	if n.Gateway != wantGateway.String() {
		return fmt.Errorf("%s: 'gateway' must be %s (first usable address of the subnet)", prefix, wantGateway)
	}
	if n.DHCPStart != "" || n.DHCPEnd != "" {
		return fmt.Errorf("%s: DHCP is not allowed for type %s", prefix, GatewayNetworkName)
	}
	if n.UseOVS {
		return fmt.Errorf("%s: 'use_ovs' is not allowed for type %s", prefix, GatewayNetworkName)
	}
	if n.NumPairs > 0 {
		return fmt.Errorf("%s: 'num_pairs' is not allowed for type %s", prefix, GatewayNetworkName)
	}
	if n.MgmtPortVFsCount != 0 {
		return fmt.Errorf("%s: 'mgmt_port_vfs_count' is not allowed for type %s", prefix, GatewayNetworkName)
	}
	if n.UplinkVFsCount != 0 {
		return fmt.Errorf("%s: 'uplink_vfs_count' is not allowed for type %s", prefix, GatewayNetworkName)
	}

	if c.IsVMMode() {
		if n.BridgeName == "" {
			return fmt.Errorf("%s: 'bridge_name' is required", prefix)
		}
		if n.Mode != "" && n.Mode != "nat" {
			return fmt.Errorf("%s: 'mode' must be 'nat' for type %s", prefix, GatewayNetworkName)
		}
		if n.AttachTo != "" && n.AttachTo != DpuType {
			return fmt.Errorf("%s: 'attach_to' must be %q for type %s", prefix, DpuType, GatewayNetworkName)
		}
	}
	return nil
}

// validateVMGateway ensures the resolved DPU/host gateway subnet is a valid
// IPv4 CIDR with capacity for host/DPU pairs, and does not overlap other
// networks that define gateway/subnet_mask.
func (c *Config) validateVMGateway() error {
	_, gw, err := net.ParseCIDR(c.DPUHostGatewaySubnet())
	if err != nil || gw.IP.To4() == nil {
		return fmt.Errorf("invalid IPv4 gateway_subnet")
	}
	if err := validateIPv4CIDRCapacity(gw.String(), 1+2*len(c.GetHostDPUPairs(""))); err != nil {
		return err
	}
	for _, n := range c.Networks {
		if n.Type == GatewayNetworkName {
			continue
		}
		if n.Gateway == "" || n.SubnetMask == "" {
			continue
		}
		prefix, err := PrefixLenFromSubnetMask(n.SubnetMask)
		if err != nil {
			return err
		}
		_, other, err := net.ParseCIDR(fmt.Sprintf("%s/%d", n.Gateway, prefix))
		if err != nil {
			return err
		}
		if gw.Contains(other.IP) || other.Contains(gw.IP) {
			return fmt.Errorf("gateway_subnet overlaps network %s", n.Name)
		}
	}
	return nil
}

// validateKindGateway checks Kind offload after legacy HostToDpu.gateway_subnet
// has been synthesized into a type: gateway network (or an explicit one was set).
func (c *Config) validateKindGateway() error {
	// Capacity for an empty legacy field (default subnet) is not checked in the
	// HostToDpu loop; re-check via the resolved gateway subnet.
	if err := validateIPv4CIDRCapacity(c.DPUHostGatewaySubnet(), 1+2*len(c.GetHostDPUPairs(""))); err != nil {
		return fmt.Errorf("gateway subnet: %w", err)
	}
	return nil
}
