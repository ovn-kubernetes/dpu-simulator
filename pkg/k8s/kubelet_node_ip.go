package k8s

import (
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/wizhao/dpu-sim/pkg/log"
	"github.com/wizhao/dpu-sim/pkg/platform"
)

const kubeadmKubeletFlagsPath = "/var/lib/kubelet/kubeadm-flags.env"

// mergeNodeIPIntoKubeadmArgsLine updates KUBELET_KUBEADM_ARGS= to use --node-ip=nodeIP,
// replacing any existing --node-ip= token. Returns the full new file content and whether
// it differs from the original.
func mergeNodeIPIntoKubeadmArgsLine(fileContent, nodeIP string) (string, bool, error) {
	if net.ParseIP(nodeIP) == nil {
		return "", false, fmt.Errorf("invalid node IP %q", nodeIP)
	}
	const prefix = "KUBELET_KUBEADM_ARGS="
	lines := strings.Split(fileContent, "\n")
	changed := false
	found := false
	for i, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		found = true
		rest := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if len(rest) < 2 || rest[0] != '"' || rest[len(rest)-1] != '"' {
			return "", false, fmt.Errorf("%s: expected quoted value, got %q", kubeadmKubeletFlagsPath, line)
		}
		inner := rest[1 : len(rest)-1]
		fields := strings.Fields(inner)
		desired := "--node-ip=" + nodeIP
		hasDesired, hasOtherNodeIP := false, false
		for _, f := range fields {
			if !strings.HasPrefix(f, "--node-ip=") {
				continue
			}
			if f == desired {
				hasDesired = true
			} else {
				hasOtherNodeIP = true
			}
		}
		if hasDesired && !hasOtherNodeIP {
			break
		}
		var kept []string
		for _, f := range fields {
			if strings.HasPrefix(f, "--node-ip=") {
				continue
			}
			kept = append(kept, f)
		}
		merged := strings.TrimSpace(strings.Join(append(kept, "--node-ip="+nodeIP), " "))
		newLine := prefix + `"` + merged + `"`
		trimmedLine := strings.TrimSpace(line)
		if newLine != trimmedLine {
			changed = true
			lines[i] = newLine
		}
		break
	}
	if !found {
		return "", false, fmt.Errorf("%s: missing %s line", kubeadmKubeletFlagsPath, prefix)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, changed, nil
}

// EnsureKubeletK8sNodeIP rewrites kubelet's kubeadm static flags so --node-ip matches k8sNodeIP
// (the address on the k8s/OVN interface from dpu-sim config). Kubelet otherwise often picks the
// libvirt mgmt DHCP address for VMs; OVN-K then mismatches host-cidrs / remote-node-ips masquerade paths
// and cross-node pod→host (and ClusterIP/NodePort→host) traffic fails while pod↔pod still works.
// In the current Kind implementation we share the same subnet for the mgmt and k8s networks.
func EnsureKubeletK8sNodeIP(cmdExec platform.CommandExecutor, k8sNodeIP string) error {
	raw, stderr, err := cmdExec.ExecuteWithTimeout("sudo cat "+kubeadmKubeletFlagsPath, 30*time.Second)
	if err != nil {
		return fmt.Errorf("read %s: %w\nstderr: %s", kubeadmKubeletFlagsPath, err, strings.TrimSpace(stderr))
	}
	newContent, changed, err := mergeNodeIPIntoKubeadmArgsLine(raw, k8sNodeIP)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(newContent))
	write := fmt.Sprintf(`echo %s | base64 -d | sudo tee %s >/dev/null`, encoded, kubeadmKubeletFlagsPath)
	if _, stderr, err := cmdExec.ExecuteWithTimeout(write, 30*time.Second); err != nil {
		return fmt.Errorf("write %s: %w\nstderr: %s", kubeadmKubeletFlagsPath, err, strings.TrimSpace(stderr))
	}
	if _, stderr, err := cmdExec.ExecuteWithTimeout("sudo systemctl restart kubelet", 3*time.Minute); err != nil {
		return fmt.Errorf("restart kubelet: %w\nstderr: %s", err, strings.TrimSpace(stderr))
	}
	log.Info("kubelet --node-ip set to %s (%s)", k8sNodeIP, cmdExec.String())
	return nil
}

// WaitAllNodesReady runs kubectl wait against the given cluster admin kubeconfig on the executor.
func WaitAllNodesReady(cmdExec platform.CommandExecutor, timeout time.Duration) error {
	script := "sudo kubectl --kubeconfig /etc/kubernetes/admin.conf wait --for=condition=Ready nodes --all --timeout=" +
		fmt.Sprintf("%ds", int(timeout.Round(time.Second)/time.Second))
	stdout, stderr, err := cmdExec.ExecuteWithTimeout(script, timeout+30*time.Second)
	if err != nil {
		return fmt.Errorf("kubectl wait nodes: %w\nstdout: %s\nstderr: %s", err, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
	}
	return nil
}
