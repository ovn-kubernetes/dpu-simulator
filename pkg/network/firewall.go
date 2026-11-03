package network

import (
	"fmt"
	"net"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

// EnableIPv4BridgeForwarding waits for the bridge sysctl node, then sets
// net.ipv4.conf.<bridge>.forwarding=1. Shared by Kind and VM gateway routing.
//
// Why: IPv4 forwarding is decided per ingress interface. A new bridge can
// inherit forwarding=0 from conf/default even when net.ipv4.ip_forward=1, so
// packets that enter the k8s underlay or DPU gateway bridge are dropped instead
// of being routed to the other side. The wait covers the race where the
// libvirt/container bridge is configured before
// /proc/sys/net/ipv4/conf/<bridge>/forwarding exists.
func EnableIPv4BridgeForwarding(cmdExec platform.CommandExecutor, bridge string) error {
	if err := waitForIPv4ForwardingSysctl(cmdExec, bridge); err != nil {
		return err
	}
	return cmdExec.RunCmd(log.LevelInfo, "sudo", "sysctl", "-w",
		fmt.Sprintf("net.ipv4.conf.%s.forwarding=1", bridge))
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

// FirewalldIsRunning reports whether firewalld appears active on the host.
func FirewalldIsRunning(cmdExec platform.CommandExecutor) bool {
	return cmdExec.RunCmd(log.LevelDebug, "firewall-cmd", "--state") == nil
}

// IptablesAcceptRuleArgs builds a filter-table ACCEPT rule for traffic from
// inBridge/src to outBridge/dst, tagged with comment for ownership.
func IptablesAcceptRuleArgs(comment, inBridge, outBridge string, src, dst *net.IPNet) []string {
	return []string{
		"-i", inBridge,
		"-o", outBridge,
		"-s", src.String(),
		"-d", dst.String(),
		"-m", "comment",
		"--comment", comment,
		"-j", "ACCEPT",
	}
}

// EnsureIptablesAcceptRule inserts the accept rule at the head of chain when absent.
func EnsureIptablesAcceptRule(
	cmdExec platform.CommandExecutor,
	chain, comment, inBridge, outBridge string,
	src, dst *net.IPNet,
) error {
	rule := IptablesAcceptRuleArgs(comment, inBridge, outBridge, src, dst)
	if IptablesRuleExists(cmdExec, chain, rule) {
		return nil
	}
	args := append([]string{"iptables", "-w", "-I", chain, "1"}, rule...)
	if err := cmdExec.RunCmd(log.LevelInfo, "sudo", args...); err != nil {
		return fmt.Errorf("add iptables accept rule on %s: %w", chain, err)
	}
	return nil
}

// DeleteIptablesAcceptRule removes every matching copy of the accept rule.
func DeleteIptablesAcceptRule(
	cmdExec platform.CommandExecutor,
	chain, comment, inBridge, outBridge string,
	src, dst *net.IPNet,
) error {
	rule := IptablesAcceptRuleArgs(comment, inBridge, outBridge, src, dst)
	for IptablesRuleExists(cmdExec, chain, rule) {
		args := append([]string{"iptables", "-w", "-D", chain}, rule...)
		if err := cmdExec.RunCmd(log.LevelDebug, "sudo", args...); err != nil {
			return fmt.Errorf("delete iptables accept rule on %s: %w", chain, err)
		}
	}
	return nil
}

// IptablesChainExists reports whether the named filter-table chain exists.
func IptablesChainExists(cmdExec platform.CommandExecutor, chain string) bool {
	return cmdExec.RunCmd(log.LevelDebug, "sudo", "iptables", "-w", "-nL", chain) == nil
}

// IptablesRuleExists reports whether rule is present in the filter-table chain.
func IptablesRuleExists(cmdExec platform.CommandExecutor, chain string, rule []string) bool {
	args := append([]string{"iptables", "-w", "-C", chain}, rule...)
	return cmdExec.RunCmd(log.LevelDebug, "sudo", args...) == nil
}
