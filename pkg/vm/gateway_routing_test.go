package vm

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/network"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
	"github.com/stretchr/testify/require"
)

func TestDedicatedGatewayGuestConfiguration(t *testing.T) {
	cfg, err := config.LoadConfig("../../config-ovnk-offload.yaml")
	require.NoError(t, err)
	m := &VMManager{config: cfg}
	for _, v := range cfg.VMs {
		xml := m.generateNetworkInterfaces(v)
		names := getInterfaceNamesAndMACs(cfg, v)
		userData := generateUserData("ssh-test", "root", "test", names, cfg, v)
		if v.Type == config.DpuType && v.Host != "" {
			require.Contains(t, xml, "source network='dpu-sim-gateway'")
			ip, err := cfg.VMGatewayIP(v.Name)
			require.NoError(t, err)
			require.Contains(t, userData, "ip addr replace "+ip+"/24 dev gateway")
			require.Contains(t, userData, "systemctl enable --now dpu-sim-dpu-gateway.service")
			require.Contains(t, userData, "Before=kubelet.service crio.service")
		} else {
			require.NotContains(t, xml, "dpu-sim-gateway")
			require.NotContains(t, userData, "dpu-sim-dpu-gateway.service")
		}
	}
	gw := cfg.GetGatewayNetwork()
	require.NotNil(t, gw)
	xml := m.generateNATNetworkXML(*gw)
	require.Contains(t, xml, "address='172.30.0.1'")
	require.NotContains(t, xml, "<dhcp>")
}

func TestExistingGatewayNetworkMustMatch(t *testing.T) {
	cfg, err := config.LoadConfig("../../config-ovnk-offload.yaml")
	require.NoError(t, err)
	m := &VMManager{config: cfg}
	gw := cfg.GetGatewayNetwork()
	require.NotNil(t, gw)
	xml := m.generateNATNetworkXML(*gw)
	require.NoError(t, validateGatewayNetworkXML(xml, *gw))
	require.Error(t, validateGatewayNetworkXML(strings.ReplaceAll(xml, "172.30.0.1", "172.30.1.1"), *gw))
	require.Error(t, validateGatewayNetworkXML(strings.ReplaceAll(xml, "</ip>", "<dhcp/></ip>"), *gw))
}

func TestOwnedNFTHandlesOnlyMatchTaggedRules(t *testing.T) {
	list := "" +
		`ip saddr 172.30.0.0/24 accept comment "` + vmGatewayRoutingComment + `" # handle 7` + "\n" +
		`accept comment "unrelated" # handle 8` + "\n" +
		`ip daddr 192.168.123.0/24 accept comment "` + vmGatewayRoutingComment + `" # handle 12`
	require.Equal(t, []string{"7", "12"}, ownedNFTHandles(list))
}

func TestIptablesForwardRuleArgsIncludeSimulatorComment(t *testing.T) {
	src := mustCIDR(t, "192.168.123.0/24")
	dst := mustCIDR(t, "172.30.0.0/24")
	args := strings.Join(network.IptablesAcceptRuleArgs(vmGatewayRoutingComment, "ovn", "dpu-sim-gw", src, dst), " ")
	require.Contains(t, args, "--comment "+vmGatewayRoutingComment)
	require.Contains(t, args, "-i ovn -o dpu-sim-gw")
	require.Contains(t, args, "-s 192.168.123.0/24 -d 172.30.0.0/24")
}

func TestGatewayRoutingCommandsAreScoped(t *testing.T) {
	kSubnet := mustCIDR(t, "192.168.123.0/24")
	gSubnet := mustCIDR(t, "172.30.0.0/24")

	t.Run("setup", func(t *testing.T) {
		fx := newGatewayRoutingFake()
		require.NoError(t, setupVMGatewayRouting(fx, "ovn", "dpu-sim-gw", kSubnet, gSubnet))
		joined := strings.Join(fx.cmds, "\n")
		require.Contains(t, joined, "nft delete rule ip libvirt_network forward handle 7")
		require.NotContains(t, joined, "handle 8")
		require.NotContains(t, joined, "flush")
		require.Contains(t, joined, "nft insert rule ip libvirt_network forward iifname ovn oifname dpu-sim-gw ip saddr 192.168.123.0/24 ip daddr 172.30.0.0/24 accept comment "+vmGatewayRoutingComment)
		require.Contains(t, joined, "iptables -w -I FORWARD 1 -i dpu-sim-gw -o ovn -s 172.30.0.0/24 -d 192.168.123.0/24")
		require.Contains(t, joined, "firewall-cmd --zone=libvirt --add-rich-rule=rule family=ipv4 source address=192.168.123.0/24 destination address=172.30.0.0/24 accept")
		require.Contains(t, joined, "sysctl -w net.ipv4.conf.ovn.forwarding=1")
		require.Contains(t, joined, "sysctl -w net.ipv4.conf.dpu-sim-gw.forwarding=1")
	})

	t.Run("cleanup", func(t *testing.T) {
		fx := newGatewayRoutingFake()
		// Pretend the FORWARD accept rules already exist so cleanup deletes them.
		fx.iptablesCheckSucceed = true
		fx.firewallQuerySucceed = true
		require.NoError(t, cleanupVMGatewayRouting(fx, "ovn", "dpu-sim-gw", kSubnet, gSubnet))
		joined := strings.Join(fx.cmds, "\n")
		require.Contains(t, joined, "nft delete rule ip libvirt_network forward handle 7")
		require.NotContains(t, joined, "handle 8")
		require.NotContains(t, joined, "nft insert")
		require.Contains(t, joined, "iptables -w -D FORWARD")
		require.NotContains(t, joined, "iptables -w -I FORWARD")
		require.Contains(t, joined, "firewall-cmd --zone=libvirt --remove-rich-rule=")
		require.NotContains(t, joined, "sysctl")
	})
}

func mustCIDR(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	require.NoError(t, err)
	return n
}

// gatewayRoutingFake records privileged commands and returns canned probe output.
type gatewayRoutingFake struct {
	cmds                 []string
	iptablesCheckSucceed bool
	firewallQuerySucceed bool
	iptablesCheckHits    map[string]int
}

func newGatewayRoutingFake() *gatewayRoutingFake {
	return &gatewayRoutingFake{iptablesCheckHits: map[string]int{}}
}

func (f *gatewayRoutingFake) record(parts ...string) {
	f.cmds = append(f.cmds, strings.Join(parts, " "))
}

func (f *gatewayRoutingFake) WaitUntilReady(time.Duration) error { return nil }

func (f *gatewayRoutingFake) Execute(command string) (string, string, error) {
	return f.ExecuteWithTimeout(command, 30*time.Second)
}

func (f *gatewayRoutingFake) ExecuteWithTimeout(command string, timeout time.Duration) (string, string, error) {
	f.cmds = append(f.cmds, command)
	switch {
	case strings.Contains(command, "nft -a list chain"):
		return "" +
			`ip saddr 172.30.0.0/24 accept comment "` + vmGatewayRoutingComment + `" # handle 7` + "\n" +
			`accept comment "unrelated" # handle 8` + "\n", "", nil
	case strings.HasPrefix(command, "command -v "):
		return strings.TrimPrefix(command, "command -v "), "", nil
	case strings.Contains(command, "firewall-cmd --get-zone-of-interface="):
		return "libvirt", "", nil
	default:
		return "", "", nil
	}
}

func (f *gatewayRoutingFake) ExecuteRetryWithTimeout(command string, interval, timeout time.Duration) (string, string, error) {
	return f.ExecuteWithTimeout(command, timeout)
}

func (f *gatewayRoutingFake) RunCmd(level log.Level, name string, args ...string) error {
	parts := append([]string{name}, args...)
	// Strip leading sudo so assertions match Kind-style command bodies.
	if len(parts) > 0 && parts[0] == "sudo" {
		parts = parts[1:]
	}
	f.record(parts...)
	if len(parts) == 0 {
		return nil
	}
	switch parts[0] {
	case "nft":
		if len(parts) >= 3 && parts[1] == "list" && parts[2] == "chain" {
			return nil
		}
		return nil
	case "iptables":
		// iptables -w -C FORWARD ...
		if len(parts) >= 4 && parts[2] == "-C" {
			key := strings.Join(parts, " ")
			if !f.iptablesCheckSucceed {
				return fmt.Errorf("missing")
			}
			// Succeed once so the delete loop terminates.
			f.iptablesCheckHits[key]++
			if f.iptablesCheckHits[key] > 1 {
				return fmt.Errorf("missing")
			}
			return nil
		}
		return nil
	case "firewall-cmd":
		if len(parts) >= 2 && parts[1] == "--state" {
			return nil
		}
		for _, a := range parts {
			if strings.HasPrefix(a, "--query-rich-rule=") {
				if f.firewallQuerySucceed {
					return nil
				}
				return fmt.Errorf("absent")
			}
		}
		return nil
	case "sysctl":
		return nil
	default:
		return nil
	}
}

func (f *gatewayRoutingFake) RunCmdInDir(level log.Level, dir string, name string, args ...string) error {
	return f.RunCmd(level, name, args...)
}

func (f *gatewayRoutingFake) RunCmdWithExtraEnv(level log.Level, extraEnv []string, name string, args ...string) error {
	return f.RunCmd(level, name, args...)
}

func (f *gatewayRoutingFake) FileExists(path string) (bool, error) {
	return strings.Contains(path, "/proc/sys/net/ipv4/conf/"), nil
}

func (f *gatewayRoutingFake) ReadDirNames(path string) ([]string, error) { return nil, nil }
func (f *gatewayRoutingFake) ReadFile(path string) ([]byte, error)       { return nil, os.ErrNotExist }
func (f *gatewayRoutingFake) WriteFile(path string, content []byte, mode os.FileMode) error {
	return nil
}
func (f *gatewayRoutingFake) RemoveAll(path string) error                     { return nil }
func (f *gatewayRoutingFake) GetDistro() (*platform.Distro, error)            { return nil, nil }
func (f *gatewayRoutingFake) GetArchitecture() (platform.Architecture, error) { return "", nil }
func (f *gatewayRoutingFake) HasSudo() bool                                   { return true }
func (f *gatewayRoutingFake) String() string                                  { return "gateway-routing-fake" }
