package kind

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

const kindContainerNetworkName = "kind"

type containerNetworkInspect struct {
	ID               string            `json:"Id"`
	IDLower          string            `json:"id"`
	Name             string            `json:"Name"`
	NameLower        string            `json:"name"`
	Driver           string            `json:"Driver"`
	DriverLower      string            `json:"driver"`
	NetworkInterface string            `json:"network_interface"`
	Options          map[string]string `json:"Options"`
	OptionsLower     map[string]string `json:"options"`
	IPAM             struct {
		Config []struct {
			Subnet string `json:"Subnet"`
		} `json:"Config"`
	} `json:"IPAM"`
	IPAMLower struct {
		Config []struct {
			Subnet string `json:"subnet"`
		} `json:"config"`
	} `json:"ipam"`
	Subnets []struct {
		Subnet string `json:"subnet"`
	} `json:"subnets"`
}

type containerNetworkInfo struct {
	name   string
	bridge string
	subnet *net.IPNet
}

type podmanInfo struct {
	Host struct {
		NetworkBackend     string `json:"networkBackend"`
		RootlessNetworkCmd string `json:"rootlessNetworkCmd"`
		Security           struct {
			Rootless bool `json:"rootless"`
		} `json:"security"`
	} `json:"host"`
}

func (m *KindManager) setupDPUGatewayRouting(cmdExec platform.CommandExecutor, gatewaySubnet *net.IPNet) error {
	kindInfo, err := m.inspectContainerNetwork(cmdExec, kindContainerNetworkName)
	if err != nil {
		return fmt.Errorf("failed to inspect Kind container network %s: %w", kindContainerNetworkName, err)
	}
	gatewayInfo, err := m.inspectContainerNetwork(cmdExec, m.config.DPUKindGatewayNetworkName())
	if err != nil {
		return fmt.Errorf("failed to inspect DPU gateway container network %s: %w", m.config.DPUKindGatewayNetworkName(), err)
	}
	if gatewayInfo.subnet == nil || gatewayInfo.subnet.String() != gatewaySubnet.String() {
		return fmt.Errorf("DPU gateway network %s has subnet %v, expected %s",
			gatewayInfo.name, gatewayInfo.subnet, gatewaySubnet.String())
	}

	if err := enableIPv4BridgeForwarding(cmdExec, kindInfo.bridge); err != nil {
		return fmt.Errorf("failed to enable IPv4 forwarding on %s: %w", kindInfo.bridge, err)
	}
	if err := enableIPv4BridgeForwarding(cmdExec, gatewayInfo.bridge); err != nil {
		return fmt.Errorf("failed to enable IPv4 forwarding on %s: %w", gatewayInfo.bridge, err)
	}

	if err := allowBridgeForwardingWithFirewalld(cmdExec, kindInfo.bridge, gatewayInfo.bridge); err != nil {
		return err
	}

	if err := ensureRawPreroutingRule(cmdExec, gatewayInfo.bridge, gatewaySubnet, kindInfo.subnet); err != nil {
		return err
	}
	if err := ensureRawPreroutingRule(cmdExec, kindInfo.bridge, kindInfo.subnet, gatewaySubnet); err != nil {
		return err
	}

	for _, chain := range m.forwardingRuleChains(cmdExec) {
		if err := ensureForwardRule(cmdExec, chain, kindInfo.bridge, gatewayInfo.bridge, kindInfo.subnet, gatewaySubnet); err != nil {
			return err
		}
		if err := ensureForwardRule(cmdExec, chain, gatewayInfo.bridge, kindInfo.bridge, gatewaySubnet, kindInfo.subnet); err != nil {
			return err
		}
	}

	log.Info("Enabled routing between Kind network %s (%s on %s) and DPU gateway network %s (%s on %s)",
		kindInfo.name, kindInfo.subnet, kindInfo.bridge, gatewayInfo.name, gatewaySubnet, gatewayInfo.bridge)
	return nil
}

func (m *KindManager) ensureGatewayRoutingSupported(cmdExec platform.CommandExecutor) error {
	if m.containerBin != "podman" {
		return nil
	}

	info, err := m.inspectPodmanInfo(cmdExec)
	if err != nil {
		return fmt.Errorf("failed to inspect podman networking: %w", err)
	}

	// Rootless podman with pasta does not expose host bridge interfaces for
	// dpu-sim to route between the kind and DPU gateway networks. This can be
	// removed once OVN-Kubernetes can provide KAPI reachability with an
	// admin-policy based route instead of host bridge forwarding.
	if podmanUsesRootlessPasta(info) {
		return fmt.Errorf("DPU gateway routing requires docker or rootful podman bridge networking; rootless podman with pasta does not expose host bridge interfaces")
	}

	return nil
}

func podmanUsesRootlessPasta(info *podmanInfo) bool {
	return info != nil && info.Host.Security.Rootless && info.Host.RootlessNetworkCmd == "pasta"
}

func (m *KindManager) inspectPodmanInfo(cmdExec platform.CommandExecutor) (*podmanInfo, error) {
	stdout, stderr, err := cmdExec.ExecuteWithTimeout("podman info --format json", 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr))
	}

	info := &podmanInfo{}
	if err := json.Unmarshal([]byte(stdout), info); err != nil {
		return nil, fmt.Errorf("failed to parse podman info JSON: %w", err)
	}
	return info, nil
}

func (m *KindManager) cleanupDPUGatewayRouting(cmdExec platform.CommandExecutor) error {
	kindInfo, kindErr := m.inspectContainerNetwork(cmdExec, kindContainerNetworkName)
	gatewayInfo, gatewayErr := m.inspectContainerNetwork(cmdExec, m.config.DPUKindGatewayNetworkName())
	if kindErr != nil || gatewayErr != nil {
		log.Debug("Skipping DPU gateway routing cleanup, missing network state: kind=%v gateway=%v", kindErr, gatewayErr)
		return nil
	}

	for _, chain := range []string{"DOCKER-USER", "FORWARD"} {
		if !iptablesChainExists(cmdExec, chain) {
			continue
		}
		deleteForwardRule(cmdExec, chain, kindInfo.bridge, gatewayInfo.bridge, kindInfo.subnet, gatewayInfo.subnet)
		deleteForwardRule(cmdExec, chain, gatewayInfo.bridge, kindInfo.bridge, gatewayInfo.subnet, kindInfo.subnet)
	}
	deleteRawPreroutingRule(cmdExec, gatewayInfo.bridge, gatewayInfo.subnet, kindInfo.subnet)
	deleteRawPreroutingRule(cmdExec, kindInfo.bridge, kindInfo.subnet, gatewayInfo.subnet)
	cleanupFirewalldBridgeForwarding(cmdExec, kindInfo.bridge, gatewayInfo.bridge)
	return nil
}

func (m *KindManager) forwardingRuleChains(cmdExec platform.CommandExecutor) []string {
	chains := []string{"FORWARD"}
	if m.containerBin == "docker" && iptablesChainExists(cmdExec, "DOCKER-USER") {
		chains = append([]string{"DOCKER-USER"}, chains...)
	}
	return chains
}

func (m *KindManager) inspectContainerNetwork(cmdExec platform.CommandExecutor, networkName string) (*containerNetworkInfo, error) {
	cmd := fmt.Sprintf("%s network inspect %s", platform.ShQuote(m.containerBin), platform.ShQuote(networkName))
	stdout, stderr, err := cmdExec.ExecuteWithTimeout(cmd, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr))
	}

	var networks []containerNetworkInspect
	if err := json.Unmarshal([]byte(stdout), &networks); err != nil {
		return nil, fmt.Errorf("failed to parse network inspect JSON: %w", err)
	}
	if len(networks) == 0 {
		return nil, fmt.Errorf("network %s was not found", networkName)
	}

	info, err := networkInfoFromInspect(networkName, networks[0])
	if err != nil {
		return nil, err
	}
	return info, nil
}

func networkInfoFromInspect(defaultName string, network containerNetworkInspect) (*containerNetworkInfo, error) {
	name := firstNonEmpty(network.Name, network.NameLower, defaultName)
	bridge := networkBridgeName(network)
	if bridge == "" {
		return nil, fmt.Errorf("could not determine bridge interface for network %s", name)
	}

	subnet, err := networkSubnet(network)
	if err != nil {
		return nil, fmt.Errorf("could not determine IPv4 subnet for network %s: %w", name, err)
	}

	return &containerNetworkInfo{name: name, bridge: bridge, subnet: subnet}, nil
}

func networkBridgeName(network containerNetworkInspect) string {
	if network.NetworkInterface != "" {
		return network.NetworkInterface
	}
	if bridge := dockerBridgeOption(network.Options); bridge != "" {
		return bridge
	}
	if bridge := dockerBridgeOption(network.OptionsLower); bridge != "" {
		return bridge
	}

	id := firstNonEmpty(network.ID, network.IDLower)
	driver := firstNonEmpty(network.Driver, network.DriverLower)
	if driver == "bridge" && len(id) >= 12 {
		return "br-" + id[:12]
	}
	return ""
}

func dockerBridgeOption(options map[string]string) string {
	if options == nil {
		return ""
	}
	return options["com.docker.network.bridge.name"]
}

func networkSubnet(network containerNetworkInspect) (*net.IPNet, error) {
	for _, config := range network.IPAM.Config {
		if subnet, err := parseIPv4CIDR(config.Subnet); err == nil {
			return subnet, nil
		}
	}
	for _, config := range network.IPAMLower.Config {
		if subnet, err := parseIPv4CIDR(config.Subnet); err == nil {
			return subnet, nil
		}
	}
	for _, subnetConfig := range network.Subnets {
		if subnet, err := parseIPv4CIDR(subnetConfig.Subnet); err == nil {
			return subnet, nil
		}
	}
	return nil, fmt.Errorf("no IPv4 subnet found")
}

func enableIPv4BridgeForwarding(cmdExec platform.CommandExecutor, bridge string) error {
	if err := waitForIPv4ForwardingSysctl(cmdExec, bridge); err != nil {
		return err
	}
	return cmdExec.RunCmd(log.LevelInfo, "sudo", "sysctl", "-w", fmt.Sprintf("net.ipv4.conf.%s.forwarding=1", bridge))
}

func waitForIPv4ForwardingSysctl(cmdExec platform.CommandExecutor, bridge string) error {
	const (
		interval = 500 * time.Millisecond
		timeout  = 10 * time.Second
	)

	path := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/forwarding", bridge)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		exists, err := cmdExec.FileExists(path)
		if err == nil && exists {
			return nil
		}
		lastErr = err
		if time.Now().Add(interval).After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("timed out waiting for %s: %w", path, lastErr)
			}
			return fmt.Errorf("timed out waiting for %s", path)
		}
		time.Sleep(interval)
	}
}

func allowBridgeForwardingWithFirewalld(cmdExec platform.CommandExecutor, bridges ...string) error {
	if !firewalldIsRunning(cmdExec) {
		return nil
	}
	for _, bridge := range bridges {
		if err := cmdExec.RunCmd(log.LevelInfo, "sudo", "firewall-cmd", "--zone=trusted", "--change-interface", bridge); err != nil {
			return fmt.Errorf("failed to trust bridge %s with firewalld: %w", bridge, err)
		}
	}
	return nil
}

func cleanupFirewalldBridgeForwarding(cmdExec platform.CommandExecutor, bridges ...string) {
	if !firewalldIsRunning(cmdExec) {
		return
	}
	for _, bridge := range bridges {
		if err := cmdExec.RunCmd(log.LevelDebug, "sudo", "firewall-cmd", "--zone=trusted", "--remove-interface", bridge); err != nil {
			log.Debug("Failed to remove bridge %s from firewalld trusted zone: %v", bridge, err)
		}
	}
}

func firewalldIsRunning(cmdExec platform.CommandExecutor) bool {
	return cmdExec.RunCmd(log.LevelDebug, "firewall-cmd", "--state") == nil
}

func ensureForwardRule(
	cmdExec platform.CommandExecutor,
	chain, inBridge, outBridge string,
	srcSubnet, dstSubnet *net.IPNet,
) error {
	rule := forwardRuleArgs(inBridge, outBridge, srcSubnet, dstSubnet)
	if iptablesRuleExists(cmdExec, chain, rule) {
		return nil
	}
	args := append([]string{"iptables", "-I", chain, "1"}, rule...)
	if err := cmdExec.RunCmd(log.LevelInfo, "sudo", args...); err != nil {
		return fmt.Errorf("failed to add iptables forwarding rule on %s: %w", chain, err)
	}
	return nil
}

func ensureRawPreroutingRule(
	cmdExec platform.CommandExecutor,
	inBridge string,
	srcSubnet, dstSubnet *net.IPNet,
) error {
	rule := rawPreroutingRuleArgs(inBridge, srcSubnet, dstSubnet)
	if iptablesRawPreroutingRuleExists(cmdExec, rule) {
		return nil
	}
	args := append([]string{"iptables", "-t", "raw", "-I", "PREROUTING", "1"}, rule...)
	if err := cmdExec.RunCmd(log.LevelInfo, "sudo", args...); err != nil {
		return fmt.Errorf("failed to add raw PREROUTING rule: %w", err)
	}
	return nil
}

func deleteRawPreroutingRule(
	cmdExec platform.CommandExecutor,
	inBridge string,
	srcSubnet, dstSubnet *net.IPNet,
) {
	rule := rawPreroutingRuleArgs(inBridge, srcSubnet, dstSubnet)
	for iptablesRawPreroutingRuleExists(cmdExec, rule) {
		args := append([]string{"iptables", "-t", "raw", "-D", "PREROUTING"}, rule...)
		if err := cmdExec.RunCmd(log.LevelDebug, "sudo", args...); err != nil {
			return
		}
	}
}

func deleteForwardRule(
	cmdExec platform.CommandExecutor,
	chain, inBridge, outBridge string,
	srcSubnet, dstSubnet *net.IPNet,
) {
	rule := forwardRuleArgs(inBridge, outBridge, srcSubnet, dstSubnet)
	for iptablesRuleExists(cmdExec, chain, rule) {
		args := append([]string{"iptables", "-D", chain}, rule...)
		if err := cmdExec.RunCmd(log.LevelDebug, "sudo", args...); err != nil {
			return
		}
	}
}

func iptablesChainExists(cmdExec platform.CommandExecutor, chain string) bool {
	return cmdExec.RunCmd(log.LevelDebug, "sudo", "iptables", "-nL", chain) == nil
}

func iptablesRuleExists(cmdExec platform.CommandExecutor, chain string, rule []string) bool {
	args := append([]string{"iptables", "-C", chain}, rule...)
	return cmdExec.RunCmd(log.LevelDebug, "sudo", args...) == nil
}

func iptablesRawPreroutingRuleExists(cmdExec platform.CommandExecutor, rule []string) bool {
	args := append([]string{"iptables", "-t", "raw", "-C", "PREROUTING"}, rule...)
	return cmdExec.RunCmd(log.LevelDebug, "sudo", args...) == nil
}

func forwardRuleArgs(inBridge, outBridge string, srcSubnet, dstSubnet *net.IPNet) []string {
	return []string{
		"-i", inBridge,
		"-o", outBridge,
		"-s", srcSubnet.String(),
		"-d", dstSubnet.String(),
		"-m", "comment",
		"--comment", "dpu-simulator-kind-gateway-routing",
		"-j", "ACCEPT",
	}
}

func rawPreroutingRuleArgs(inBridge string, srcSubnet, dstSubnet *net.IPNet) []string {
	return []string{
		"-i", inBridge,
		"-s", srcSubnet.String(),
		"-d", dstSubnet.String(),
		"-m", "comment",
		"--comment", "dpu-simulator-kind-gateway-routing",
		"-j", "ACCEPT",
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
