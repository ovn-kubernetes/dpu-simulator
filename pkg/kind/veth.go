package kind

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/network"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

const (
	// Format strings for veth endpoint names on the host namespace (before
	// they are moved into containers and renamed).
	vethHostEndFmt = "host%d-eth0-%d" // host-side data veth: host{pairIdx}-eth0-{i}
	vethDpuEndFmt  = "dpu%d-rep0-%d"  // DPU-side data veth:  dpu{pairIdx}-rep0-{i}
)

// SetupHostToDpuNetwork reads the HostToDpu network config and creates veth
// channels between host and DPU Kind containers (which may be in different clusters).
func (m *KindManager) SetupHostToDpuNetwork() error {
	h2dCfg := m.config.GetHostToDpuNetwork()
	if h2dCfg == nil {
		return nil
	}

	pairs := m.config.GetHostDPUPairs("")
	if len(pairs) == 0 {
		log.Warn("HostToDpu network configured but no host/dpu worker pairs found in Kind node config")
		return nil
	}

	numPairs := m.config.GetHostToDpuNumPairs()
	hostExec := platform.NewLocalExecutor()
	m.CleanupVethTopology(hostExec, pairs, numPairs)
	return m.CreateVethTopology(hostExec, pairs, numPairs)
}

// CleanupVethTopology removes leftover veth interfaces from a previous incomplete run.
// When veths are in kind containers, they are automatically cleaned up by the kind delete command.
// When a container is destroyed, its network namespace is torn down by the kernel which automatically
// deletes the interfaces in that namespace.
func (m *KindManager) CleanupVethTopology(cmdExec platform.CommandExecutor, pairs []config.HostDPUPair, numPairs int) {
	for pairIdx := range pairs {
		for i := 0; i < numPairs; i++ {
			cmdExec.RunCmd(log.LevelDebug, "sudo", "ip", "link", "delete", fmt.Sprintf(vethHostEndFmt, pairIdx, i))
		}
	}
}

// CreateVethTopology creates veth pair channels for each host-DPU pair.
// numPairs controls how many data channels are created per pair.
//
// For each pair the following interfaces are created:
//
// Host container:  eth0-0 … eth0-(numPairs-1) e.g. eth0-0, eth0-1, …, eth0-15
// DPU  container:  rep0-0 … rep0-(numPairs-1) e.g. rep0-0, rep0-1, …, rep0-15
//
// A separate management veth pair is also created for each pair:
//
// Host container:  pf     (takes over the Kind IP from eth0)
// DPU  container:  pfrep
func (m *KindManager) CreateVethTopology(cmdExec platform.CommandExecutor, pairs []config.HostDPUPair, numPairs int) error {
	gatewaySubnet, usedGatewayIPs, err := m.prepareDPUGatewayNetwork(cmdExec, pairs)
	if err != nil {
		return err
	}

	for pairIdx, pair := range pairs {
		log.Info("Setting up veth topology for pair %d: %s <-> %s (%d data channels)",
			pairIdx, pair.HostNode, pair.DPUNode, numPairs)

		hostPID, err := getContainerPID(cmdExec, m.containerBin, pair.HostNode)
		if err != nil {
			return fmt.Errorf("failed to get PID for host container %s: %w", pair.HostNode, err)
		}
		dpuPID, err := getContainerPID(cmdExec, m.containerBin, pair.DPUNode)
		if err != nil {
			return fmt.Errorf("failed to get PID for DPU container %s: %w", pair.DPUNode, err)
		}

		hostContainerExec := platform.NewDockerExecutor(pair.HostNode, m.containerBin)
		dpuContainerExec := platform.NewDockerExecutor(pair.DPUNode, m.containerBin)

		if err := createDataVeths(cmdExec, hostContainerExec, dpuContainerExec, pair.HostNode, pair.DPUNode, pairIdx, hostPID, dpuPID, numPairs); err != nil {
			return fmt.Errorf("failed to create data veths for pair %d: %w", pairIdx, err)
		}

		if m.config.IsOffloadDPU() && gatewaySubnet != nil {
			gwIP, allocErr := network.GetFreeIPv4AddressInSubnet(gatewaySubnet, usedGatewayIPs)
			if allocErr != nil {
				return fmt.Errorf("failed to allocate gateway veth IP for pair %d: %w", pairIdx, allocErr)
			}
			if err := assignDpuHostGatewayIP(hostContainerExec, pair.HostNode, gwIP, gatewaySubnet); err != nil {
				return fmt.Errorf("failed to assign gateway veth IP for pair %d: %w", pairIdx, err)
			}
			usedGatewayIPs = append(usedGatewayIPs, gwIP)
		}
	}

	log.Info("✓ Veth topology created for %d host-DPU pairs (%d data channels each)", len(pairs), numPairs)
	return nil
}

// prepareDPUGatewayNetwork ensures the shared gateway bridge exists, connects
// each DPU container to it, and returns the subnet plus IPs already consumed by
// the bridge gateway and DPU containers. Host-side gateway veth allocation uses
// the returned used IP list to avoid collisions.
func (m *KindManager) prepareDPUGatewayNetwork(cmdExec platform.CommandExecutor, pairs []config.HostDPUPair) (*net.IPNet, []net.IP, error) {
	if !m.config.IsOffloadDPU() {
		return nil, nil, nil
	}

	gatewaySubnet, err := parseIPv4CIDR(m.config.DPUHostGatewaySubnet())
	if err != nil {
		return nil, nil, fmt.Errorf("invalid DPU host gateway subnet: %w", err)
	}

	if err := m.ensureDPUGatewayNetwork(cmdExec, gatewaySubnet); err != nil {
		return nil, nil, err
	}

	usedIPs := []net.IP{containerBridgeGatewayIP(gatewaySubnet)}
	for _, pair := range pairs {
		ip, err := m.ensureDPUConnectedToGatewayNetwork(cmdExec, pair.DPUNode)
		if err != nil {
			return nil, nil, err
		}
		usedIPs = append(usedIPs, ip)
	}

	return gatewaySubnet, usedIPs, nil
}

// ensureDPUGatewayNetwork creates the Docker/Podman bridge network used for
// simulated DPU gateway traffic. The network must use the configured gateway
// subnet because OVN-Kubernetes programs gateway router external addresses from
// that same subnet.
func (m *KindManager) ensureDPUGatewayNetwork(cmdExec platform.CommandExecutor, subnet *net.IPNet) error {
	networkName := m.config.DPUKindGatewayNetworkName()
	inspectCmd := fmt.Sprintf("%s network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' %s",
		platform.ShQuote(m.containerBin), platform.ShQuote(networkName))
	if stdout, _, err := cmdExec.Execute(inspectCmd); err == nil {
		if !strings.Contains(stdout, subnet.String()) {
			return fmt.Errorf("DPU gateway network %s already exists with subnet %q, expected %s",
				networkName, strings.TrimSpace(stdout), subnet.String())
		}
		return nil
	}

	log.Info("Creating DPU gateway network %s (%s)", networkName, subnet.String())
	if err := cmdExec.RunCmd(log.LevelInfo, m.containerBin, "network", "create", "--subnet", subnet.String(), networkName); err != nil {
		return fmt.Errorf("failed to create DPU gateway network %s: %w", networkName, err)
	}
	return nil
}

// ensureDPUConnectedToGatewayNetwork attaches a DPU Kind container to the
// gateway bridge network and returns the container IP allocated by the
// container runtime. That IP is reserved so the paired host gateway veth does
// not receive an overlapping address from the same subnet.
func (m *KindManager) ensureDPUConnectedToGatewayNetwork(cmdExec platform.CommandExecutor, dpuNode string) (net.IP, error) {
	networkName := m.config.DPUKindGatewayNetworkName()
	ip, err := m.getContainerNetworkIP(cmdExec, dpuNode, networkName)
	if err == nil {
		return ip, nil
	}

	log.Info("Connecting DPU node %s to gateway network %s", dpuNode, networkName)
	if err := cmdExec.RunCmd(log.LevelInfo, m.containerBin, "network", "connect", networkName, dpuNode); err != nil {
		return nil, fmt.Errorf("failed to connect DPU node %s to gateway network %s: %w", dpuNode, networkName, err)
	}

	ip, err = m.waitForContainerNetworkIP(cmdExec, dpuNode, networkName)
	if err != nil {
		return nil, fmt.Errorf("failed to read DPU node %s IP on gateway network %s: %w", dpuNode, networkName, err)
	}
	return ip, nil
}

func (m *KindManager) waitForContainerNetworkIP(cmdExec platform.CommandExecutor, containerName, networkName string) (net.IP, error) {
	const (
		interval = 500 * time.Millisecond
		timeout  = 10 * time.Second
	)

	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ip, err := m.getContainerNetworkIP(cmdExec, containerName, networkName)
		if err == nil {
			return ip, nil
		}
		lastErr = err
		if time.Now().Add(interval).After(deadline) {
			return nil, fmt.Errorf("timed out after %s: %w", timeout, lastErr)
		}
		time.Sleep(interval)
	}
}

// createDataVeths creates numPairs veth pairs and moves them into the
// containers. Host side is renamed to eth0-{i}, DPU side to rep0-{i}.
func createDataVeths(
	hostExec platform.CommandExecutor,
	hostContainerExec, dpuContainerExec platform.CommandExecutor,
	hostNode, dpuNode string,
	pairIdx int, hostPID, dpuPID string, numPairs int,
) error {
	for i := 0; i < numPairs; i++ {
		hostEnd := fmt.Sprintf(vethHostEndFmt, pairIdx, i)
		dpuEnd := fmt.Sprintf(vethDpuEndFmt, pairIdx, i)

		if err := hostExec.RunCmd(log.LevelDebug, "sudo", "ip", "link", "add", hostEnd, "type", "veth", "peer", "name", dpuEnd); err != nil {
			return fmt.Errorf("failed to create veth pair %s <-> %s: %w", hostEnd, dpuEnd, err)
		}

		if err := hostExec.RunCmd(log.LevelDebug, "sudo", "ip", "link", "set", hostEnd, "netns", hostPID); err != nil {
			return fmt.Errorf("failed to move %s to host container: %w", hostEnd, err)
		}
		if err := hostExec.RunCmd(log.LevelDebug, "sudo", "ip", "link", "set", dpuEnd, "netns", dpuPID); err != nil {
			return fmt.Errorf("failed to move %s to DPU container: %w", dpuEnd, err)
		}

		hostTarget := fmt.Sprintf(network.HostDataIfFmt, i)
		if err := hostContainerExec.RunCmd(log.LevelDebug, "ip", "link", "set", hostEnd, "name", hostTarget); err != nil {
			return fmt.Errorf("failed to rename %s to %s in host container: %w", hostEnd, hostTarget, err)
		}
		hostMAC := network.GenerateMACForHostToDpu(hostNode, config.HostType, i)
		if err := hostContainerExec.RunCmd(log.LevelDebug, "ip", "link", "set", hostTarget, "address", hostMAC); err != nil {
			return fmt.Errorf("failed to set MAC %s on %s in host container: %w", hostMAC, hostTarget, err)
		}
		if err := hostContainerExec.RunCmd(log.LevelDebug, "ip", "link", "set", hostTarget, "up"); err != nil {
			return fmt.Errorf("failed to bring up %s in host container: %w", hostTarget, err)
		}

		dpuTarget := fmt.Sprintf(network.DPUDataIfFmt, i)
		if err := dpuContainerExec.RunCmd(log.LevelDebug, "ip", "link", "set", dpuEnd, "name", dpuTarget); err != nil {
			return fmt.Errorf("failed to rename %s to %s in DPU container: %w", dpuEnd, dpuTarget, err)
		}
		dpuMAC := network.GenerateMACForHostToDpu(dpuNode, config.DpuType, i)
		if err := dpuContainerExec.RunCmd(log.LevelDebug, "ip", "link", "set", dpuTarget, "address", dpuMAC); err != nil {
			return fmt.Errorf("failed to set MAC %s on %s in DPU container: %w", dpuMAC, dpuTarget, err)
		}
		if err := dpuContainerExec.RunCmd(log.LevelDebug, "ip", "link", "set", dpuTarget, "up"); err != nil {
			return fmt.Errorf("failed to bring up %s in DPU container: %w", dpuTarget, err)
		}
	}
	return nil
}

// GetKindSubnetAndAllocatedIPs discovers the Kind network subnet and all
// already-used IPs by reading eth0 inside every node across all Kind clusters.
func (m *KindManager) GetKindSubnetAndAllocatedIPs() (*net.IPNet, []net.IP) {
	var subnet *net.IPNet
	var usedIPs []net.IP

	clusters, err := m.provider.List()
	if err != nil {
		log.Warn("Could not list Kind clusters: %v", err)
		return nil, nil
	}

	for _, clusterName := range clusters {
		nodes, err := m.provider.ListNodes(clusterName)
		if err != nil {
			log.Warn("Could not list nodes for cluster %s: %v", clusterName, err)
			continue
		}
		for _, node := range nodes {
			name := node.String()
			ip, ipNet, err := getKindNodeNetworkCIDR(platform.NewDockerExecutor(name, m.containerBin))
			if err != nil {
				log.Warn("Could not read eth0 from %s: %v", name, err)
				continue
			}
			usedIPs = append(usedIPs, ip)
			if subnet == nil {
				subnet = ipNet
			}
		}
	}

	if subnet == nil {
		log.Warn("Could not determine Kind network subnet")
	}
	return subnet, usedIPs
}

// getKindNodeNetworkCIDR reads the IPv4 address and subnet of eth0 inside a
// container which by default is part of the default Kind network.
func getKindNodeNetworkCIDR(exec platform.CommandExecutor) (net.IP, *net.IPNet, error) {
	stdout, _, err := exec.Execute("ip -4 -o addr show eth0 | awk '{print $4}'")
	if err != nil {
		return nil, nil, err
	}
	cidr := strings.TrimSpace(stdout)
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CIDR %q: %w", cidr, err)
	}
	return ip, ipNet, nil
}

// assignGatewayVethIP assigns gwIP to eth0-0 inside the host container. This
// address is on the DPU gateway subnet, separate from the Kind/KAPI subnet on
// eth0.
func assignDpuHostGatewayIP(hostContainerExec platform.CommandExecutor, hostNode string, gwIP net.IP, subnet *net.IPNet) error {
	ones, _ := subnet.Mask.Size()
	gwCIDR := fmt.Sprintf("%s/%d", gwIP, ones)
	gwIf := fmt.Sprintf(network.HostDataIfFmt, 0)

	if err := hostContainerExec.RunCmd(log.LevelDebug, "ip", "addr", "add", gwCIDR, "dev", gwIf); err != nil {
		return fmt.Errorf("failed to assign %s to %s in %s: %w", gwCIDR, gwIf, hostNode, err)
	}

	log.Info("Assigned %s to %s in %s", gwCIDR, gwIf, hostNode)
	return nil
}

func getContainerPID(cmdExec platform.CommandExecutor, containerBin, container string) (string, error) {
	inspectOut, _, err := cmdExec.Execute(fmt.Sprintf(
		"%s inspect %s", containerBin, container))
	if err != nil {
		return "", fmt.Errorf("failed to inspect container %s: %w", container, err)
	}

	var containers []struct {
		State struct {
			Pid int `json:"Pid"`
		} `json:"State"`
	}
	if err := json.Unmarshal([]byte(inspectOut), &containers); err != nil {
		return "", fmt.Errorf("failed to parse inspect JSON for %s: %w", container, err)
	}
	if len(containers) == 0 || containers[0].State.Pid == 0 {
		return "", fmt.Errorf("container %s has no running PID", container)
	}
	return fmt.Sprintf("%d", containers[0].State.Pid), nil
}
