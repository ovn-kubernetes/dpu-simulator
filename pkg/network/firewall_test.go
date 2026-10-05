package network

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
	"github.com/stretchr/testify/require"
)

func TestIptablesAcceptRuleArgs(t *testing.T) {
	src := mustCIDR(t, "172.18.0.0/16")
	dst := mustCIDR(t, "172.30.0.0/24")
	args := strings.Join(IptablesAcceptRuleArgs("dpu-simulator-test", "br-a", "br-b", src, dst), " ")
	require.Equal(t,
		"-i br-a -o br-b -s 172.18.0.0/16 -d 172.30.0.0/24 -m comment --comment dpu-simulator-test -j ACCEPT",
		args)
}

func TestEnsureAndDeleteIptablesAcceptRule(t *testing.T) {
	src := mustCIDR(t, "192.168.123.0/24")
	dst := mustCIDR(t, "172.30.0.0/24")
	fx := &firewallFake{}

	require.NoError(t, EnsureIptablesAcceptRule(fx, "FORWARD", "tag", "ovn", "dpu-sim-gw", src, dst))
	require.NoError(t, EnsureIptablesAcceptRule(fx, "FORWARD", "tag", "ovn", "dpu-sim-gw", src, dst))
	require.NoError(t, DeleteIptablesAcceptRule(fx, "FORWARD", "tag", "ovn", "dpu-sim-gw", src, dst))

	joined := strings.Join(fx.cmds, "\n")
	require.Contains(t, joined, "iptables -w -C FORWARD")
	require.Contains(t, joined, "iptables -w -I FORWARD 1 -i ovn -o dpu-sim-gw")
	require.Contains(t, joined, "iptables -w -D FORWARD -i ovn -o dpu-sim-gw")
	// Second ensure should not insert again while the rule is recorded as present.
	inserts := 0
	for _, c := range fx.cmds {
		if strings.Contains(c, "-I FORWARD 1") {
			inserts++
		}
	}
	require.Equal(t, 1, inserts)
}

func TestEnableIPv4BridgeForwarding(t *testing.T) {
	fx := &firewallFake{files: map[string]bool{
		"/proc/sys/net/ipv4/conf/br0/forwarding": true,
	}}
	require.NoError(t, EnableIPv4BridgeForwarding(fx, "br0"))
	require.Contains(t, strings.Join(fx.cmds, "\n"), "sysctl -w net.ipv4.conf.br0.forwarding=1")
}

func TestFirewalldIsRunning(t *testing.T) {
	ok := &firewallFake{firewalldRunning: true}
	require.True(t, FirewalldIsRunning(ok))
	down := &firewallFake{firewalldRunning: false}
	require.False(t, FirewalldIsRunning(down))
}

func TestIptablesChainExists(t *testing.T) {
	fx := &firewallFake{chains: map[string]bool{"FORWARD": true, "DOCKER-USER": true}}
	require.True(t, IptablesChainExists(fx, "FORWARD"))
	require.False(t, IptablesChainExists(fx, "MISSING"))
}

func mustCIDR(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	require.NoError(t, err)
	return n
}

type firewallFake struct {
	cmds             []string
	files            map[string]bool
	chains           map[string]bool
	rules            map[string]bool
	firewalldRunning bool
}

func (f *firewallFake) key(chain string, rule []string) string {
	return chain + " " + strings.Join(rule, " ")
}

func (f *firewallFake) WaitUntilReady(time.Duration) error { return nil }

func (f *firewallFake) Execute(command string) (string, string, error) {
	return f.ExecuteWithTimeout(command, 30*time.Second)
}

func (f *firewallFake) ExecuteWithTimeout(command string, timeout time.Duration) (string, string, error) {
	f.cmds = append(f.cmds, command)
	return "", "", nil
}

func (f *firewallFake) ExecuteRetryWithTimeout(command string, interval, timeout time.Duration) (string, string, error) {
	return f.ExecuteWithTimeout(command, timeout)
}

func (f *firewallFake) RunCmd(level log.Level, name string, args ...string) error {
	parts := append([]string{name}, args...)
	if len(parts) > 0 && parts[0] == "sudo" {
		parts = parts[1:]
	}
	f.cmds = append(f.cmds, strings.Join(parts, " "))
	if len(parts) == 0 {
		return nil
	}
	switch parts[0] {
	case "firewall-cmd":
		if len(parts) >= 2 && parts[1] == "--state" {
			if f.firewalldRunning {
				return nil
			}
			return fmt.Errorf("not running")
		}
	case "iptables":
		// iptables -w -nL CHAIN | -w -C CHAIN ... | -w -I/-D ...
		if len(parts) >= 4 && parts[2] == "-nL" {
			if f.chains[parts[3]] {
				return nil
			}
			return fmt.Errorf("missing chain")
		}
		if len(parts) >= 4 && parts[2] == "-C" {
			chain := parts[3]
			rule := parts[4:]
			if f.rules == nil {
				f.rules = map[string]bool{}
			}
			if f.rules[f.key(chain, rule)] {
				return nil
			}
			return fmt.Errorf("missing")
		}
		if len(parts) >= 5 && parts[2] == "-I" {
			chain := parts[3]
			rule := parts[5:] // skip position
			if f.rules == nil {
				f.rules = map[string]bool{}
			}
			f.rules[f.key(chain, rule)] = true
			return nil
		}
		if len(parts) >= 4 && parts[2] == "-D" {
			chain := parts[3]
			rule := parts[4:]
			if f.rules == nil {
				f.rules = map[string]bool{}
			}
			delete(f.rules, f.key(chain, rule))
			return nil
		}
	}
	return nil
}

func (f *firewallFake) RunCmdInDir(level log.Level, dir string, name string, args ...string) error {
	return f.RunCmd(level, name, args...)
}

func (f *firewallFake) RunCmdWithExtraEnv(level log.Level, extraEnv []string, name string, args ...string) error {
	return f.RunCmd(level, name, args...)
}

func (f *firewallFake) FileExists(path string) (bool, error) {
	if f.files != nil && f.files[path] {
		return true, nil
	}
	return false, nil
}

func (f *firewallFake) ReadDirNames(path string) ([]string, error) { return nil, nil }
func (f *firewallFake) ReadFile(path string) ([]byte, error)       { return nil, os.ErrNotExist }
func (f *firewallFake) WriteFile(path string, content []byte, mode os.FileMode) error {
	return nil
}
func (f *firewallFake) RemoveAll(path string) error                     { return nil }
func (f *firewallFake) GetDistro() (*platform.Distro, error)            { return nil, nil }
func (f *firewallFake) GetArchitecture() (platform.Architecture, error) { return "", nil }
func (f *firewallFake) HasSudo() bool                                   { return true }
func (f *firewallFake) String() string                                  { return "firewall-fake" }
