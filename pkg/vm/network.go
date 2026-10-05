package vm

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/network"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

// NetworkExists checks if a network exists
func (m *VMManager) NetworkExists(networkName string) bool {
	net, err := m.conn.LookupNetworkByName(networkName)
	if err != nil {
		return false
	}
	defer net.Free()
	return true
}

// CreateNetwork creates a libvirt network based on the configuration
func (m *VMManager) CreateNetwork(netCfg config.NetworkConfig) error {
	if m.NetworkExists(netCfg.Name) {
		// Gateway networks are reused by name on redeploy, but DPU guests get
		// static IPs/routes from config. Reject a leftover libvirt network whose
		// XML does not match, and start it if it is defined but inactive.
		if netCfg.Type == config.GatewayNetworkName {
			existing, err := m.conn.LookupNetworkByName(netCfg.Name)
			if err != nil {
				return err
			}
			defer existing.Free()
			raw, err := existing.GetXMLDesc(0)
			if err != nil {
				return err
			}
			if err := validateGatewayNetworkXML(raw, netCfg); err != nil {
				return err
			}
			active, err := existing.IsActive()
			if err != nil {
				return err
			}
			if !active {
				return existing.Create()
			}
		}
		// Keep network creation idempotent for repeated deploy/redeploy runs.
		log.Info("Network %s already exists, skipping creation", netCfg.Name)
		return nil
	}

	// Generate network XML based on Mode
	var xml string

	switch netCfg.Mode {
	case "nat":
		xml = m.generateNATNetworkXML(netCfg)
	case "l2-bridge":
		if netCfg.UseOVS {
			// For OVS networks, create the OVS bridge first, then the libvirt network
			if err := CreateOVSBridge(m.hostExec, netCfg.BridgeName); err != nil {
				return fmt.Errorf("failed to create OVS bridge %s: %w", netCfg.BridgeName, err)
			}
			xml = generateOVSNetworkXML(netCfg.Name, netCfg.BridgeName)
		} else {
			xml = generateBridgeNetworkXML(netCfg.Name, netCfg.BridgeName)
		}
	default:
		return fmt.Errorf("unsupported network mode: %s", netCfg.Mode)
	}

	net, err := m.conn.NetworkDefineXML(xml)
	if err != nil {
		return fmt.Errorf("failed to define network %s: %w", netCfg.Name, err)
	}
	defer net.Free()

	if err := net.SetAutostart(true); err != nil {
		return fmt.Errorf("failed to set autostart for network %s: %w", netCfg.Name, err)
	}

	if err := net.Create(); err != nil {
		return fmt.Errorf("failed to start network %s: %w", netCfg.Name, err)
	}

	log.Info("✓ Created network: %s", netCfg.Name)
	return nil
}

// EnsureHostNetworkPrerequisites configures host-side libvirt networking prerequisites.
// This must run before creating a libvirt connection used by VMManager;
// applying these host-level settings later during network creation was too late
// on some Debian/Ubuntu nftables hosts and caused NAT bring-up failures.
func EnsureHostNetworkPrerequisites(cmdExec platform.CommandExecutor) error {
	if err := ensureLibvirtNATFirewallBackend(cmdExec); err != nil {
		return fmt.Errorf("failed to prepare libvirt firewall backend for NAT networks: %w", err)
	}
	return nil
}

// ensureLibvirtNATFirewallBackend prepares Debian-like hosts for stable
// libvirt NAT network creation.
//
// Why: on some Debian/Ubuntu systems, libvirt NAT setup can fail when the
// host iptables alternative and libvirt firewall backend are not aligned.
//
// Scope: no-op for non-Debian-like distros.
func ensureLibvirtNATFirewallBackend(cmdExec platform.CommandExecutor) error {
	distro, err := platform.GetHostDistro()
	if err != nil {
		return fmt.Errorf("failed to detect host distro: %w", err)
	}
	if !distro.IsDebianLike() {
		return nil
	}

	legacyChanged, err := ensureIptablesLegacyForLibvirtNAT(cmdExec)
	if err != nil {
		return err
	}

	const confPath = "/etc/libvirt/network.conf"

	raw, err := os.ReadFile(confPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read %s: %w", confPath, err)
	}

	updated, changed := setLibvirtFirewallBackend(string(raw), "nftables")
	if !changed && !legacyChanged {
		return nil
	}

	tmpFile, err := os.CreateTemp("", "dpu-sim-libvirt-network-*.conf")
	if err != nil {
		return fmt.Errorf("failed to create temp file for %s: %w", confPath, err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(updated); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write temp libvirt config: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp libvirt config: %w", err)
	}

	out, errOut, err := platform.RunCommandInDir(cmdExec, "", "sudo", []string{"install", "-m", "0644", tmpPath, confPath}, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to install %s: %w, output: %s", confPath, err, platform.CombinedCmdOutput(out, errOut))
	}

	if err := restartLibvirtForNetworkFirewallChange(cmdExec); err != nil {
		return err
	}

	if changed {
		log.Info("Configured libvirt firewall backend to nftables for Debian-like host")
	}
	if legacyChanged {
		log.Info("Configured iptables alternatives to legacy for libvirt NAT on Debian-like host")
	}
	return nil
}

// ensureIptablesLegacyForLibvirtNAT switches iptables/ip6tables alternatives to
// legacy only when the host reports the known nftables NAT incompatibility.
//
// This was observed in our VM bring-up on Ubuntu 22.04.5 LTS with errors like:
// "table `nat` is incompatible, use 'nft' tool" during libvirt NAT probing.
func ensureIptablesLegacyForLibvirtNAT(cmdExec platform.CommandExecutor) (bool, error) {
	probeOut, probeErr, _ := platform.RunCommandInDir(cmdExec, "", "iptables", []string{"-w", "--table", "nat", "--list-rules"}, 30*time.Second)
	combined := platform.CombinedCmdOutput(probeOut, probeErr)
	const natIncompatMarker = "table `nat' is incompatible, use 'nft' tool"
	if !strings.Contains(combined, natIncompatMarker) {
		return false, nil
	}

	pairs := []struct {
		name string
		path string
	}{
		{name: "iptables", path: "/usr/sbin/iptables-legacy"},
		{name: "ip6tables", path: "/usr/sbin/ip6tables-legacy"},
	}

	changed := false
	for _, p := range pairs {
		if _, err := os.Stat(p.path); err != nil {
			continue
		}

		verOut, verErr, err := platform.RunCommandInDir(cmdExec, "", p.name, []string{"--version"}, 30*time.Second)
		if err == nil && strings.Contains(strings.ToLower(platform.CombinedCmdOutput(verOut, verErr)), "legacy") {
			continue
		}

		so, se, err := platform.RunCommandInDir(cmdExec, "", "sudo", []string{"update-alternatives", "--set", p.name, p.path}, 2*time.Minute)
		if err != nil {
			return changed, fmt.Errorf("failed to set %s alternative to %s: %w, output: %s", p.name, p.path, err, platform.CombinedCmdOutput(so, se))
		}
		changed = true
	}

	return changed, nil
}

// restartLibvirtForNetworkFirewallChange restarts the first available libvirt
// service that manages networking after firewall backend updates.
// Different distros expose different service names (virtnetworkd/libvirtd).
func restartLibvirtForNetworkFirewallChange(cmdExec platform.CommandExecutor) error {
	services := []string{"virtnetworkd", "libvirtd"}
	for _, svc := range services {
		so, se, err := platform.RunCommandInDir(cmdExec, "", "sudo", []string{"systemctl", "restart", svc}, 3*time.Minute)
		if err == nil {
			log.Debug("Restarted %s after libvirt firewall backend update", svc)
			return nil
		}
		log.Debug("Failed restarting %s: %v (output: %s)", svc, err, strings.TrimSpace(platform.CombinedCmdOutput(so, se)))
	}

	return fmt.Errorf("failed to restart libvirt service after updating firewall backend")
}

// setLibvirtFirewallBackend updates firewall_backend in libvirt network.conf
// and returns the updated content plus whether a change was required.
func setLibvirtFirewallBackend(conf, backend string) (string, bool) {
	desired := fmt.Sprintf("firewall_backend = %q", backend)
	lines := strings.Split(conf, "\n")
	updated := make([]string, 0, len(lines)+1)
	seen := false
	changed := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "firewall_backend") {
			seen = true
			if trimmed != desired {
				updated = append(updated, desired)
				changed = true
			} else {
				updated = append(updated, line)
			}
			continue
		}
		updated = append(updated, line)
	}

	if !seen {
		if len(updated) > 0 && strings.TrimSpace(updated[len(updated)-1]) != "" {
			updated = append(updated, "")
		}
		updated = append(updated, desired)
		changed = true
	}

	result := strings.Join(updated, "\n")
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}

	return result, changed
}

// buildDHCPReservations returns DHCP host entries (MAC -> IP) for the given network.
// Only the k8s network uses static reservations (K8sNodeMAC/K8sNodeIP). The mgmt
// network uses dynamic DHCP only for now.
func (m *VMManager) buildDHCPReservations(netCfg config.NetworkConfig) string {
	if netCfg.Type != config.K8sNetworkName {
		return ""
	}
	var sb strings.Builder
	for _, vmCfg := range m.config.VMs {
		if vmCfg.K8sNodeMAC != "" && vmCfg.K8sNodeIP != "" {
			sb.WriteString(fmt.Sprintf("      <host mac='%s' name='%s' ip='%s'/>\n", vmCfg.K8sNodeMAC, vmCfg.Name, vmCfg.K8sNodeIP))
		}
	}
	return sb.String()
}

// generateNATNetworkXML generates XML for a NAT network with Linux bridges and DHCP
func (m *VMManager) generateNATNetworkXML(netCfg config.NetworkConfig) string {
	var sb strings.Builder

	sb.WriteString("<network>\n")
	sb.WriteString(fmt.Sprintf("  <name>%s</name>\n", netCfg.Name))
	sb.WriteString("  <forward mode='nat'/>\n")
	sb.WriteString(fmt.Sprintf("  <bridge name='%s' stp='on' delay='0'/>\n", netCfg.BridgeName))

	sb.WriteString(fmt.Sprintf("  <ip address='%s' netmask='%s'>\n", netCfg.Gateway, netCfg.SubnetMask))

	// K8s (ovn-network): no DHCP on the segment; VMs assign k8s interface IP manually
	// Gateway (dpu-sim-gateway): no DHCP on the segment; VMs assign gateway interface IP manually
	if netCfg.Type != config.K8sNetworkName && netCfg.Type != config.GatewayNetworkName {
		sb.WriteString("    <dhcp>\n")
		if netCfg.DHCPStart != "" && netCfg.DHCPEnd != "" {
			sb.WriteString(fmt.Sprintf("      <range start='%s' end='%s'/>\n", netCfg.DHCPStart, netCfg.DHCPEnd))
		}
		sb.WriteString(m.buildDHCPReservations(netCfg))
		sb.WriteString("    </dhcp>\n")
	}

	sb.WriteString("  </ip>\n")

	sb.WriteString("</network>\n")
	return sb.String()
}

// generateBridgeNetworkXML generates XML for a simple Linux bridge network
func generateBridgeNetworkXML(networkName, bridgeName string) string {
	var sb strings.Builder

	sb.WriteString("<network>\n")
	sb.WriteString(fmt.Sprintf("  <name>%s</name>\n", networkName))
	sb.WriteString(fmt.Sprintf("  <bridge name='%s' stp='on' delay='0'/>\n", bridgeName))
	sb.WriteString("</network>\n")

	return sb.String()
}

// generateOVSNetworkXML generates XML for an OVS-based bridge network
func generateOVSNetworkXML(networkName, bridgeName string) string {
	var sb strings.Builder

	sb.WriteString("<network>\n")
	sb.WriteString(fmt.Sprintf("  <name>%s</name>\n", networkName))
	sb.WriteString("  <forward mode='bridge'/>\n")
	sb.WriteString(fmt.Sprintf("  <bridge name='%s'/>\n", bridgeName))
	sb.WriteString("  <virtualport type='openvswitch'/>\n")
	sb.WriteString("</network>\n")

	return sb.String()
}

// CreateOVSBridge creates an OVS bridge
func CreateOVSBridge(cmdExec platform.CommandExecutor, bridgeName string) error {
	_, _, err := platform.RunCommandInDir(cmdExec, "", "ovs-vsctl", []string{"br-exists", bridgeName}, 30*time.Second)
	if err == nil {
		log.Info("OVS bridge %s already exists, skipping creation", bridgeName)
		return nil
	}

	co, ce, err := platform.RunCommandInDir(cmdExec, "", "ovs-vsctl", []string{"add-br", bridgeName}, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to create OVS bridge %s: %w, output: %s", bridgeName, err, platform.CombinedCmdOutput(co, ce))
	}

	bo, be, err := platform.RunCommandInDir(cmdExec, "", "ip", []string{"link", "set", bridgeName, "up"}, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to bring up OVS bridge %s: %w, output: %s", bridgeName, err, platform.CombinedCmdOutput(bo, be))
	}

	log.Info("✓ Created OVS bridge: %s", bridgeName)
	return nil
}

// DeleteOVSBridge deletes an OVS bridge
func DeleteOVSBridge(cmdExec platform.CommandExecutor, bridgeName string) error {
	_, _, err := platform.RunCommandInDir(cmdExec, "", "ovs-vsctl", []string{"br-exists", bridgeName}, 30*time.Second)
	if err != nil {
		log.Info("OVS bridge %s doesn't exist, skipping deletion", bridgeName)
		return nil
	}

	do, de, err := platform.RunCommandInDir(cmdExec, "", "ovs-vsctl", []string{"del-br", bridgeName}, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to delete OVS bridge %s: %w, output: %s", bridgeName, err, platform.CombinedCmdOutput(do, de))
	}

	return nil
}

// CreateHostToDPUNetwork creates a single network channel for a host-to-DPU
// connection. index identifies the channel within the pair; OVS is used for the bridge.
func (m *VMManager) CreateHostToDPUNetwork(hostName, dpuName string, index int) error {
	networkName := network.GetHostToDPUNetworkName(hostName, dpuName, index)
	bridgeName := network.GenerateBridgeName(hostName, dpuName, index)

	if m.NetworkExists(networkName) {
		return fmt.Errorf("host-to-DPU network %s already exists", networkName)
	}

	if err := CreateOVSBridge(m.hostExec, bridgeName); err != nil {
		return fmt.Errorf("failed to create OVS bridge for host-to-DPU: %w", err)
	}

	xml := generateOVSNetworkXML(networkName, bridgeName)

	net, err := m.conn.NetworkDefineXML(xml)
	if err != nil {
		return fmt.Errorf("failed to define host-to-DPU network %s: %w", networkName, err)
	}
	defer net.Free()

	if err := net.SetAutostart(true); err != nil {
		return fmt.Errorf("failed to set autostart for network %s: %w", networkName, err)
	}

	if err := net.Create(); err != nil {
		return fmt.Errorf("failed to start network %s: %w", networkName, err)
	}

	log.Info("✓ Created host-to-DPU network: %s (bridge: %s)", networkName, bridgeName)
	return nil
}

// CreateAllNetworks creates all networks defined in the configuration and
// host-to-DPU networks. The number of channels per host-DPU pair is
// controlled by the HostToDpu network config's num_pairs field.
func (m *VMManager) CreateAllNetworks() error {
	log.Info("=== Creating Networks ===")

	// Create configured networks (skip HostToDpu which is handled below)
	for _, netCfg := range m.config.Networks {
		if netCfg.Type == config.HostToDpuNetworkType {
			continue
		}
		if err := m.CreateNetwork(netCfg); err != nil {
			return fmt.Errorf("failed to create network %s: %w", netCfg.Name, err)
		}
	}

	if err := m.configureGatewayRouting(false); err != nil {
		return err
	}

	// Create host-to-DPU network channels
	numPairs := m.config.GetHostToDpuNumPairs()
	mappings := m.config.GetHostDPUMappings()
	for _, mapping := range mappings {
		for _, dpuConn := range mapping.Connections {
			for idx := 0; idx < numPairs; idx++ {
				if err := m.CreateHostToDPUNetwork(mapping.Host.Name, dpuConn.DPU.Name, idx); err != nil {
					return fmt.Errorf("failed to create host-to-DPU network (pair %d) for host %s and DPU %s: %w",
						idx, mapping.Host.Name, dpuConn.DPU.Name, err)
				}
			}
		}
	}

	log.Info("✓ All networks created successfully")
	return nil
}

// DeleteNetwork removes a libvirt network by name
func (m *VMManager) DeleteNetwork(networkName string) error {
	net, err := m.conn.LookupNetworkByName(networkName)
	if err != nil {
		// Network doesn't exist, nothing to do
		return nil
	}
	defer net.Free()

	active, err := net.IsActive()
	if err != nil {
		return fmt.Errorf("failed to check if network is active: %w", err)
	}

	if active {
		if err := net.Destroy(); err != nil {
			return fmt.Errorf("failed to destroy network: %w", err)
		}
	}

	if err := net.Undefine(); err != nil {
		return fmt.Errorf("failed to undefine network: %w", err)
	}

	return nil
}

// CleanupNetworks removes all networks defined in the configuration
func (m *VMManager) CleanupNetworks() error {
	log.Info("=== Cleaning up Networks ===")

	errors := make([]string, 0)

	if err := m.configureGatewayRouting(true); err != nil {
		log.Error("✗ Failed to clean up gateway routing: %v", err)
		errors = append(errors, fmt.Sprintf("failed to clean up gateway routing: %v", err))
	}

	// Cleanup configured networks
	for _, netCfg := range m.config.Networks {
		netName := netCfg.Name
		log.Debug("Cleaning up network: %s...", netName)

		if err := m.DeleteNetwork(netName); err != nil {
			log.Error("✗ Failed to remove network %s: %v", netName, err)
			errors = append(errors, fmt.Sprintf("failed to remove network %s: %v", netName, err))
			continue
		}

		if netCfg.UseOVS {
			log.Debug("Cleaning up OVS bridge: %s...", netCfg.BridgeName)
			if err := DeleteOVSBridge(m.hostExec, netCfg.BridgeName); err != nil {
				log.Error("✗ Failed to remove OVS bridge %s: %v", netCfg.BridgeName, err)
				errors = append(errors, fmt.Sprintf("failed to remove OVS bridge %s: %v", netCfg.BridgeName, err))
				continue
			}
		}

		log.Info("✓ Removed network %s", netName)
	}

	// Cleanup host-to-DPU networks (one per channel per pair)
	numPairs := m.config.GetHostToDpuNumPairs()
	mappings := m.config.GetHostDPUMappings()
	for _, mapping := range mappings {
		for _, dpuConn := range mapping.Connections {
			for i := 0; i < numPairs; i++ {
				netName := network.GetHostToDPUNetworkName(mapping.Host.Name, dpuConn.DPU.Name, i)
				bridgeName := network.GenerateBridgeName(mapping.Host.Name, dpuConn.DPU.Name, i)
				log.Debug("Cleaning up host-to-DPU network: %s...", netName)

				if err := m.DeleteNetwork(netName); err != nil {
					log.Error("✗ Failed to remove network %s: %v", netName, err)
					errors = append(errors, fmt.Sprintf("failed to remove network %s: %v", netName, err))
					continue
				}

				log.Debug("Cleaning up host-to-DPU OVS bridge: %s...", bridgeName)
				if err := DeleteOVSBridge(m.hostExec, bridgeName); err != nil {
					log.Error("✗ Failed to remove OVS bridge %s: %v", bridgeName, err)
					errors = append(errors, fmt.Sprintf("failed to remove OVS bridge %s: %v", bridgeName, err))
					continue
				}

				log.Info("✓ Removed host-to-DPU network %s (bridge: %s)", netName, bridgeName)
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup networks errors: %s", strings.Join(errors, "; "))
	}

	return nil
}

// Refuse to silently reuse a gateway network left with a different address plan.
func validateGatewayNetworkXML(raw string, want config.NetworkConfig) error {
	var existing struct {
		Bridge struct {
			Name string `xml:"name,attr"`
		} `xml:"bridge"`
		Forward struct {
			Mode string `xml:"mode,attr"`
		} `xml:"forward"`
		IP []struct {
			Address string    `xml:"address,attr"`
			Netmask string    `xml:"netmask,attr"`
			DHCP    *struct{} `xml:"dhcp"`
		} `xml:"ip"`
	}
	if err := xml.Unmarshal([]byte(raw), &existing); err != nil {
		return err
	}
	mismatch := existing.Bridge.Name != want.BridgeName ||
		existing.Forward.Mode != "nat" ||
		len(existing.IP) != 1 ||
		existing.IP[0].Address != want.Gateway ||
		existing.IP[0].Netmask != want.SubnetMask ||
		existing.IP[0].DHCP != nil
	if mismatch {
		return fmt.Errorf("existing gateway network %s differs from the configured topology; rebuild the deployment", want.Name)
	}
	return nil
}
