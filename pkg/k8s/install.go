// Package k8s provides functions to install Kubernetes on a machine
package k8s

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/linux"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/network"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

// InstallKubernetes installs Kubernetes on a machine (baremetal or VM)
// The executor should already be ready (WaitUntilReady called by the caller if needed)
func (m *K8sMachineManager) InstallKubernetes(cmdExec platform.CommandExecutor, machineName, k8sVersion string) error {
	log.Info("Installing Kubernetes on %s (%s)...", machineName, cmdExec.String())

	if err := linux.SetHostname(cmdExec, machineName); err != nil {
		return fmt.Errorf("failed to set hostname for Kubernetes: %w", err)
	}

	reason := "Required for Kubernetes installation"
	deps := []platform.Dependency{
		{
			Name:        "Swap Off",
			Reason:      reason,
			CheckFunc:   linux.CheckSwapDisabled,
			InstallFunc: linux.DisableSwap,
		},
		{
			Name:        "K8s Kernel Modules",
			Reason:      reason,
			CheckFunc:   linux.CheckK8sKernelModules,
			InstallFunc: linux.ConfigureK8sKernelModules,
		},
		{
			Name:        "crio",
			Reason:      reason,
			CheckCmd:    []string{"systemctl", "is-active", "crio"},
			InstallFunc: linux.InstallCRIO,
		},
		{
			Name:        "openvswitch",
			Reason:      reason,
			CheckCmd:    []string{"ovs-vsctl", "--version"},
			InstallFunc: linux.InstallSystemdOpenVSwitch,
		},
		{
			Name:        "NetworkManager-ovs",
			Reason:      reason,
			CheckFunc:   linux.CheckGenericPackage,
			InstallFunc: linux.InstallNetworkManagerOpenVSwitch,
		},
		{
			Name:        "Kubelet Tools",
			Reason:      reason,
			CheckCmd:    []string{"kubeadm", "version", "-o", "short"},
			InstallFunc: linux.InstallKubelet,
		},
		{
			Name:        "Disable firewalld",
			Reason:      reason,
			CheckFunc:   linux.CheckFirewallDisabled,
			InstallFunc: linux.DisableFirewall,
		},
	}
	if err := platform.EnsureDependenciesWithExecutor(cmdExec, deps, m.config); err != nil {
		return fmt.Errorf("failed to ensure dependencies: %w", err)
	}
	if err := linux.EnsureCRIOCNIPluginPaths(cmdExec); err != nil {
		return fmt.Errorf("failed to ensure CRI-O CNI plugin paths: %w", err)
	}
	if err := linux.ConfigureCRIOLocalRegistry(cmdExec, m.config); err != nil {
		return fmt.Errorf("failed to configure local registry for CRI-O: %w", err)
	}
	if err := cmdExec.RunCmd(log.LevelDebug, "sudo", "systemctl", "restart", "crio"); err != nil {
		return fmt.Errorf("failed to restart CRI-O after dependency and registry setup: %w", err)
	}

	log.Info("✓ Kubernetes %s installed on %s", k8sVersion, machineName)
	return nil
}

// EnsureOVNBridges creates OVS bridges and grants the daemon access to its
// runtime directory. Fedora drops ovs-vswitchd to the openvswitch user, while
// OVN containers chown the shared directory to root. Named ACLs survive that
// ownership change, allowing sockets for dynamically added gateway/UDN bridges.
func (m *K8sMachineManager) EnsureOVNBridges(cmdExec platform.CommandExecutor, bridges ...string) error {
	if err := platform.EnsureDependenciesWithExecutor(cmdExec, []platform.Dependency{{
		Name: "acl", Reason: "Unprivileged OVS runtime socket access",
		CheckCmd: []string{"setfacl", "--version"}, InstallFunc: linux.InstallGenericPackage,
	}}, m.config); err != nil {
		return fmt.Errorf("failed to install OVS runtime ACL support: %w", err)
	}
	const dropInDir = "/etc/systemd/system/ovs-vswitchd.service.d"
	if err := cmdExec.RunCmd(log.LevelDebug, "sudo", "mkdir", "-p", dropInDir, "/usr/local/libexec"); err != nil {
		return fmt.Errorf("failed to create OVS service drop-in directory: %w", err)
	}
	const accessScriptPath = "/usr/local/libexec/dpu-sim-ovs-runtime-access"
	const accessScript = `#!/bin/sh
set -eu
ovs_pid=$(cat /run/openvswitch/ovs-vswitchd.pid)
ovs_uid=$(stat -c %u "/proc/$ovs_pid")
setfacl -m "u:$ovs_uid:rwx,d:u:$ovs_uid:rwx" /run/openvswitch
find /run/openvswitch -maxdepth 1 -type s -exec setfacl -m "u:$ovs_uid:rw" '{}' +
`
	if err := writePrivilegedFile(cmdExec, accessScriptPath, []byte(accessScript), 0o755); err != nil {
		return fmt.Errorf("failed to write OVS runtime ACL helper: %w", err)
	}
	const dropIn = "[Service]\nExecStartPost=" + accessScriptPath + "\n"
	if err := writePrivilegedFile(cmdExec, dropInDir+"/10-dpu-sim-runtime.conf", []byte(dropIn), 0o644); err != nil {
		return fmt.Errorf("failed to configure OVS runtime ownership: %w", err)
	}
	if err := cmdExec.RunCmd(log.LevelDebug, "sudo", "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload OVS service configuration: %w", err)
	}
	for _, br := range bridges {
		if err := cmdExec.RunCmd(log.LevelDebug, "sudo", "ovs-vsctl", "--may-exist", "add-br", br); err != nil {
			return fmt.Errorf("failed to create %s: %w", br, err)
		}
	}
	if err := cmdExec.RunCmd(log.LevelDebug, "sudo", "systemctl", "restart", "openvswitch"); err != nil {
		return fmt.Errorf("failed to restart openvswitch (needed so bridge datapaths are created): %w", err)
	}
	return nil
}

// writePrivilegedFile writes the payload as the connected user, then installs
// it at its privileged destination. SSHExecutor.WriteFile performs the shell
// redirection as the connected user, so writing directly to /etc fails for
// non-root SSH accounts even when sudo is available.
func writePrivilegedFile(cmdExec platform.CommandExecutor, path string, content []byte, mode os.FileMode) error {
	stdout, stderr, err := cmdExec.Execute("mktemp /tmp/dpu-sim-ovs.XXXXXX")
	if err != nil {
		return fmt.Errorf("create temporary file: %w: %s", err, strings.TrimSpace(stderr))
	}
	tmpPath := strings.TrimSpace(stdout)
	if tmpPath == "" {
		return fmt.Errorf("create temporary file returned an empty path")
	}
	defer func() { _ = cmdExec.RemoveAll(tmpPath) }()

	if err := cmdExec.WriteFile(tmpPath, content, mode); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := cmdExec.RunCmd(log.LevelDebug, "sudo", "install", "-m", fmt.Sprintf("%o", mode.Perm()), tmpPath, path); err != nil {
		return fmt.Errorf("install privileged file: %w", err)
	}
	return nil
}

// EnsureOVNBrInt creates br-int and restarts openvswitch.
// Convenience wrapper around EnsureOVNBridges for non-DPU nodes.
func (m *K8sMachineManager) EnsureOVNBrInt(cmdExec platform.CommandExecutor) error {
	return m.EnsureOVNBridges(cmdExec, "br-int")
}

func (m *K8sMachineManager) SetupOVNBrEx(cmdExec platform.CommandExecutor, mgmtIP string, k8sIP string) error {
	log.Info("--- Setting up OVN br-ex on %s (%s) ---", mgmtIP, cmdExec.String())

	mgmtInterfaceInfo, err := network.GetInterfaceByIP(cmdExec, mgmtIP)
	if err != nil {
		return fmt.Errorf("failed to get interface information: %w", err)
	}
	log.Info("Mgmt Interface information: %s", mgmtInterfaceInfo.String())

	k8sInterfaceInfo, err := network.GetInterfaceByIP(cmdExec, k8sIP)
	if err != nil {
		return fmt.Errorf("failed to get interface information: %w", err)
	}
	log.Info("K8s Interface information: %s", k8sInterfaceInfo.String())

	sb := strings.Builder{}
	sb.WriteString("set -e\n")
	sb.WriteString("BRIDGE_NAME=br-ex\n")
	sb.WriteString(fmt.Sprintf("IF1=%s\n", mgmtInterfaceInfo.Name))
	sb.WriteString("IF1_CONN=$(nmcli -g GENERAL.CONNECTION device show $IF1 2>/dev/null || echo '')\n")
	// Check if IF1_CONN is a valid NetworkManager connection
	sb.WriteString("IF1_CONN_EXISTS=$(nmcli -g NAME connection show \"$IF1_CONN\" 2>/dev/null || echo '')\n")
	sb.WriteString(fmt.Sprintf("IF2=%s\n", k8sInterfaceInfo.Name))
	sb.WriteString("IF2_MAC=$(cat /sys/class/net/$IF2/address)\n")
	// Get the actual connection name for IF2 (may differ from interface name)
	sb.WriteString("IF2_CONN=$(nmcli -g GENERAL.CONNECTION device show $IF2 2>/dev/null || echo '')\n")

	sb.WriteString("nmcli c add type ovs-bridge conn.interface $BRIDGE_NAME con-name $BRIDGE_NAME\n")
	sb.WriteString("nmcli c add type ovs-port conn.interface $BRIDGE_NAME master $BRIDGE_NAME con-name ovs-port-$BRIDGE_NAME\n")
	sb.WriteString("nmcli c add type ovs-interface slave-type ovs-port conn.interface $BRIDGE_NAME master ovs-port-$BRIDGE_NAME con-name ovs-if-$BRIDGE_NAME\n")
	sb.WriteString("nmcli c add type ovs-port conn.interface $IF2 master $BRIDGE_NAME con-name ovs-port-$IF2\n")
	sb.WriteString("nmcli c add type ethernet conn.interface $IF2 master ovs-port-$IF2 con-name ovs-if-$IF2\n")
	// Only delete the old connection if it exists and is not empty
	sb.WriteString("if [ -n \"$IF2_CONN\" ] && [ \"$IF2_CONN\" != \"--\" ]; then nmcli conn delete \"$IF2_CONN\"; fi\n")
	sb.WriteString("sudo ip addr flush dev $IF2\n")
	sb.WriteString("nmcli conn mod $BRIDGE_NAME connection.autoconnect yes\n")
	sb.WriteString("nmcli conn mod ovs-if-$IF2 connection.autoconnect yes\n")
	sb.WriteString("nmcli conn mod ovs-port-$IF2 connection.autoconnect yes\n")
	sb.WriteString("nmcli conn mod ovs-if-$BRIDGE_NAME connection.autoconnect yes\n")
	sb.WriteString("nmcli conn mod ovs-port-$BRIDGE_NAME connection.autoconnect yes\n")

	// Set the br-ex interface to use DHCP
	sb.WriteString("nmcli conn mod ovs-if-$BRIDGE_NAME ipv4.method auto\n")
	sb.WriteString("nmcli conn mod ovs-if-$BRIDGE_NAME ipv4.route-metric 50\n")

	// Set the br-ex interface to be the default route
	sb.WriteString("nmcli conn mod ovs-if-$BRIDGE_NAME ipv4.never-default no\n")

	// Set the MAC address of the br-ex interface to the MAC address of the IF2 interface
	// to get the same DHCP lease on the br-ex interface as the IF2 interface
	sb.WriteString("nmcli conn mod ovs-if-$BRIDGE_NAME 802-3-ethernet.cloned-mac-address $IF2_MAC\n")

	// Make sure the MGMT interface is not the default route (only if IF1_CONN is a valid NM connection)
	sb.WriteString("if [ -n \"$IF1_CONN_EXISTS\" ]; then\n")
	sb.WriteString("  nmcli conn mod \"$IF1_CONN\" ipv4.never-default yes\n")
	sb.WriteString("  nmcli conn mod \"$IF1_CONN\" ipv4.ignore-auto-dns yes\n")
	sb.WriteString("  nmcli conn up \"$IF1_CONN\"\n")
	sb.WriteString("fi\n")

	// Activate the OVS connections immediately (order matters: bridge first, then ports)
	sb.WriteString("nmcli conn up $BRIDGE_NAME\n")
	sb.WriteString("nmcli conn up ovs-if-$IF2\n")
	sb.WriteString("nmcli conn up ovs-port-$IF2\n")
	sb.WriteString("nmcli conn up ovs-if-$BRIDGE_NAME\n")
	sb.WriteString("nmcli conn up ovs-port-$BRIDGE_NAME\n")

	// Known issue for br-int bridge (not properly created by OVN)
	sb.WriteString("ovs-vsctl add-br br-int\n")

	// Set br-ex as the external bridge for OVN
	sb.WriteString("sudo ovs-vsctl set open_vswitch . external-ids:ovn-bridge-mappings=\"physnet1:br-ex\"\n")

	stdout, stderr, err := cmdExec.ExecuteWithTimeout(sb.String(), 5*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to setup OVN br-ex: %w, stdout: %s, stderr: %s", err, stdout, stderr)
	}
	return nil
}

// PrintOVNBrExStatus prints the status of the OVN br-ex bridge
// Does not return an error, just prints the status.
func (m *K8sMachineManager) PrintOVNBrExStatus(cmdExec platform.CommandExecutor) {
	log.Debug("\n========== OVN/OVS Status on %s ==========", cmdExec)
	log.Debug("--- OVS Bridges ---")
	stdout, stderr, err := cmdExec.ExecuteWithTimeout("sudo ovs-vsctl list-br", 30*time.Second)
	if err != nil {
		log.Error("Error listing bridges: %s", stderr)
	} else {
		log.Debug("%s", strings.TrimSpace(stdout))
	}

	log.Debug("--- br-ex Ports ---")
	stdout, stderr, err = cmdExec.ExecuteWithTimeout("sudo ovs-vsctl list-ports br-ex 2>/dev/null || echo 'br-ex not found'", 30*time.Second)
	if err != nil {
		log.Error("Error: %s", stderr)
	} else {
		log.Debug("%s", strings.TrimSpace(stdout))
	}

	log.Debug("--- OVS Show (Full Config) ---")
	stdout, stderr, err = cmdExec.ExecuteWithTimeout("sudo ovs-vsctl show", 30*time.Second)
	if err != nil {
		log.Error("Error: %s", stderr)
	} else {
		log.Debug("%s", strings.TrimSpace(stdout))
	}

	stdout, stderr, err = cmdExec.ExecuteWithTimeout("ip route show", 30*time.Second)
	if err != nil {
		log.Error("Error: %s", stderr)
	} else {
		log.Debug("%s", strings.TrimSpace(stdout))
	}

	log.Debug("--- br-ex Linux Interface ---")
	stdout, stderr, err = cmdExec.ExecuteWithTimeout("ip addr show br-ex 2>/dev/null || echo 'br-ex interface not found'", 30*time.Second)
	if err != nil {
		log.Error("Error: %s", stderr)
	} else {
		log.Debug("%s", strings.TrimSpace(stdout))
	}

	log.Debug("--- NetworkManager Connections ---")
	stdout, stderr, err = cmdExec.ExecuteWithTimeout("nmcli connection show 2>/dev/null || echo 'nmcli not available'", 30*time.Second)
	if err != nil {
		log.Error("Error: %s", stderr)
	} else {
		log.Debug("%s", strings.TrimSpace(stdout))
	}

	log.Debug("--- OVS External IDs (Open_vSwitch) ---")
	stdout, stderr, err = cmdExec.ExecuteWithTimeout("sudo ovs-vsctl get Open_vSwitch . external_ids", 30*time.Second)
	if err != nil {
		log.Error("Error: %s", stderr)
	} else {
		log.Debug("%s", strings.TrimSpace(stdout))
	}

	log.Debug("--- br-ex External IDs ---")
	stdout, stderr, err = cmdExec.ExecuteWithTimeout("sudo ovs-vsctl get Bridge br-ex external_ids 2>/dev/null || echo 'br-ex not found'", 30*time.Second)
	if err != nil {
		log.Error("Error: %s", stderr)
	} else {
		log.Debug("%s", strings.TrimSpace(stdout))
	}

	log.Debug("--- br-int External IDs ---")
	stdout, stderr, err = cmdExec.ExecuteWithTimeout("sudo ovs-vsctl get Bridge br-int external_ids 2>/dev/null || echo 'br-int not found'", 30*time.Second)
	if err != nil {
		log.Error("Error: %s", stderr)
	} else {
		log.Debug("%s", strings.TrimSpace(stdout))
	}

	log.Debug("==========================================")
}

func (m *K8sMachineManager) SetupKubectlForRootUser(cmdExec platform.CommandExecutor, machineName string) error {
	log.Info("Setting up kubectl on %s (%s)...", machineName, cmdExec.String())

	sb := strings.Builder{}
	sb.WriteString("set -e\n")
	sb.WriteString("mkdir -p /root/.kube\n")
	sb.WriteString("sudo cp /etc/kubernetes/admin.conf /root/.kube/config\n")
	sb.WriteString("sudo chown root:root /root/.kube/config\n")

	stdout, stderr, err := cmdExec.ExecuteWithTimeout(sb.String(), 1*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to setup kubectl: %w, stdout: %s, stderr: %s", err, stdout, stderr)
	}
	return nil
}

// WaitForControlPlaneReady waits until bootstrap-critical control plane components
// are ready enough for additional nodes to join (CSR approval/signing path available).
func (m *K8sMachineManager) WaitForControlPlaneReady(cmdExec platform.CommandExecutor) error {
	sb := strings.Builder{}
	sb.WriteString("set -e\n")
	sb.WriteString("KUBECTL='sudo kubectl --kubeconfig /etc/kubernetes/admin.conf'\n")
	sb.WriteString("$KUBECTL wait --for=condition=Ready node/$(hostname) --timeout=5m\n")
	sb.WriteString("$KUBECTL wait --for=condition=Ready pod -n kube-system -l component=kube-controller-manager --timeout=5m\n")

	stdout, stderr, err := cmdExec.ExecuteWithTimeout(sb.String(), 6*time.Minute)
	if err != nil {
		return fmt.Errorf("failed waiting for control plane readiness: %w, stdout: %s, stderr: %s", err, stdout, stderr)
	}

	return nil
}

// ExtractWorkerJoinCommand extracts the worker join command from the machine
func (m *K8sMachineManager) ExtractWorkerJoinCommand(cmdExec platform.CommandExecutor, machineName string) (string, error) {
	sb := strings.Builder{}
	sb.WriteString("set -e\n")
	sb.WriteString("sudo kubeadm token create --print-join-command\n")

	stdout, stderr, err := cmdExec.ExecuteWithTimeout(sb.String(), 1*time.Minute)
	if err != nil {
		return "", fmt.Errorf("failed to extract join command: %w, stderr: %s", err, stderr)
	}
	workerJoinCommand := strings.TrimSpace(stdout)
	return workerJoinCommand, nil
}

// GenerateCertificateKey generates a new certificate key for control plane joins
func (m *K8sMachineManager) GenerateCertificateKey(cmdExec platform.CommandExecutor, machineName string) (string, error) {
	// Generate a new certificate key for control plane joins
	sb := strings.Builder{}
	sb.WriteString("set -e\n")
	sb.WriteString("sudo kubeadm init phase upload-certs --upload-certs 2>/dev/null | tail -1\n")

	stdout, stderr, err := cmdExec.ExecuteWithTimeout(sb.String(), 1*time.Minute)
	if err != nil {
		return "", fmt.Errorf("failed to get certificate key: %w, stderr: %s", err, stderr)
	}
	certificateKey := strings.TrimSpace(stdout)

	return certificateKey, nil
}

// InitializeControlPlane initializes a Kubernetes control plane node and
// returns the join/kubeconfig data needed by additional masters and workers.
//
// cmdExec: remote command executor for the target node.
// machineName: logical node name for logging/errors.
// k8sIP: node IP advertised by kube-apiserver.
// podCIDR: cluster pod network CIDR passed to kubeadm init.
// serviceCIDR: cluster service network CIDR passed to kubeadm init.
// controlPlaneEndpoint: stable API endpoint used by join commands (when set),
// avoiding joins tied to one node IP in hybrid/multi-network topologies.
// extraAPIServerSANs: additional cert SANs so API access remains TLS-valid
// across alternate node IPs/hostnames used in hybrid or multi-network setups.
// skipKubeProxy omits the addon when OVN provides service routing.
func (m *K8sMachineManager) InitializeControlPlane(cmdExec platform.CommandExecutor, machineName, k8sIP, podCIDR, serviceCIDR, controlPlaneEndpoint string, extraAPIServerSANs []string, skipKubeProxy bool) (*ControlPlaneInfo, error) {
	log.Info("Initializing control plane on %s (%s)...", machineName, cmdExec.String())
	log.Info("K8s IP: %s Pod CIDR: %s, Service CIDR: %s", k8sIP, podCIDR, serviceCIDR)

	sb := strings.Builder{}
	sb.WriteString("set -e\n")
	// Use --upload-certs to enable control plane join for additional masters
	initCmd := fmt.Sprintf("sudo kubeadm init --pod-network-cidr=%s --service-cidr=%s --apiserver-advertise-address=%s", podCIDR, serviceCIDR, k8sIP)
	if skipKubeProxy {
		initCmd += " --skip-phases=addon/kube-proxy"
	}
	if strings.TrimSpace(controlPlaneEndpoint) != "" {
		initCmd += fmt.Sprintf(" --control-plane-endpoint=%s", strings.TrimSpace(controlPlaneEndpoint))
	}
	if len(extraAPIServerSANs) > 0 {
		sans := make([]string, 0, len(extraAPIServerSANs))
		for _, san := range extraAPIServerSANs {
			san = strings.TrimSpace(san)
			if san == "" {
				continue
			}
			sans = append(sans, san)
		}
		if len(sans) > 0 {
			initCmd += fmt.Sprintf(" --apiserver-cert-extra-sans=%s", strings.Join(sans, ","))
		}
	}
	initCmd += " --upload-certs"
	sb.WriteString(initCmd + "\n")

	stdout, stderr, err := cmdExec.ExecuteWithTimeout(sb.String(), 10*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("control plane initialization failed: %w, stderr: %s", err, stderr)
	}

	log.Debug("Control plane initialization output: %s", stdout)

	if err := m.SetupKubectlForRootUser(cmdExec, machineName); err != nil {
		return nil, fmt.Errorf("failed to setup kubectl for root user: %w", err)
	}

	if err := m.WaitForControlPlaneReady(cmdExec); err != nil {
		return nil, fmt.Errorf("control plane is not ready for node joins: %w", err)
	}

	workerJoinCommand, err := m.ExtractWorkerJoinCommand(cmdExec, machineName)
	if err != nil {
		return nil, fmt.Errorf("failed to extract worker join command: %w", err)
	}

	certificateKey, err := m.GenerateCertificateKey(cmdExec, machineName)
	if err != nil {
		return nil, fmt.Errorf("failed to generate certificate key: %w", err)
	}

	// Build the control plane join command
	controlPlaneJoinCommand := fmt.Sprintf("%s --control-plane --certificate-key %s", workerJoinCommand, certificateKey)

	// Get the kubeconfig for API access
	kubeconfig, err := m.getKubeconfigContent(cmdExec)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// Build the API server endpoint
	apiServerEndpoint := fmt.Sprintf("https://%s:6443", k8sIP)
	if strings.TrimSpace(controlPlaneEndpoint) != "" {
		apiServerEndpoint = fmt.Sprintf("https://%s", strings.TrimSpace(controlPlaneEndpoint))
	}

	joinInfo := &ControlPlaneInfo{
		WorkerJoinCommand:       workerJoinCommand,
		ControlPlaneJoinCommand: controlPlaneJoinCommand,
		APIServerEndpoint:       apiServerEndpoint,
		Kubeconfig:              kubeconfig,
	}

	log.Info("✓ Control plane initialized on %s", machineName)
	log.Info("Worker join command: %s", workerJoinCommand)
	log.Info("Control plane join command: %s", controlPlaneJoinCommand)
	log.Info("API server endpoint: %s", apiServerEndpoint)
	return joinInfo, nil
}

// JoinControlPlane joins an additional control plane node to a Kubernetes cluster
func (m *K8sMachineManager) JoinControlPlane(cmdExec platform.CommandExecutor, machineName string, joinInfo *ControlPlaneInfo) error {
	log.Debug("Joining control plane node %s to Kubernetes cluster...", machineName)

	sb := strings.Builder{}
	sb.WriteString("set -e\n")
	sb.WriteString(fmt.Sprintf("sudo %s\n", joinInfo.ControlPlaneJoinCommand))

	stdout, stderr, err := cmdExec.ExecuteWithTimeout(sb.String(), 5*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to join control plane node: %w, stderr: %s", err, stderr)
	}

	if err := m.SetupKubectlForRootUser(cmdExec, machineName); err != nil {
		return fmt.Errorf("failed to setup kubectl for root user: %w", err)
	}

	log.Info("✓ Control plane node joined to Kubernetes cluster: %s", machineName)
	log.Debug("Join command output: %s", stdout)
	return nil
}

// JoinWorker joins a worker node to a Kubernetes cluster
func (m *K8sMachineManager) JoinWorker(cmdExec platform.CommandExecutor, machineName string, joinInfo *ControlPlaneInfo) error {
	log.Debug("Joining worker node %s to Kubernetes cluster...", machineName)

	sb := strings.Builder{}
	sb.WriteString("set -e\n")
	sb.WriteString(fmt.Sprintf("sudo %s\n", joinInfo.WorkerJoinCommand))

	stdout, stderr, err := cmdExec.ExecuteWithTimeout(sb.String(), 5*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to join worker node: %w, stderr: %s", err, stderr)
	}

	log.Info("✓ Worker node joined to Kubernetes cluster: %s", machineName)
	log.Debug("Join command output: %s", stdout)
	return nil
}

// getKubeconfigContent retrieves the kubeconfig content from a control plane node
func (m *K8sMachineManager) getKubeconfigContent(cmdExec platform.CommandExecutor) (string, error) {
	script := "sudo cat /etc/kubernetes/admin.conf"

	stdout, stderr, err := cmdExec.ExecuteWithTimeout(script, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("failed to get kubeconfig: %w, stderr: %s", err, stderr)
	}

	return strings.TrimSpace(stdout), nil
}

// GetKubeconfig retrieves the kubeconfig from a control plane node and saves it to a file
func (m *K8sMachineManager) GetKubeconfig(cmdExec platform.CommandExecutor, outputPath string) error {
	kubeconfig, err := m.getKubeconfigContent(cmdExec)
	if err != nil {
		return err
	}

	// Write to file
	if err := os.WriteFile(outputPath, []byte(kubeconfig), 0o600); err != nil {
		return fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	log.Info("✓ Kubeconfig saved to: %s", outputPath)
	return nil
}

// SaveKubeconfigToFile saves kubeconfig content to a file in the specified directory
// The file is saved as <kubeconfigDir>/<clusterName>.kubeconfig
func SaveKubeconfigToFile(kubeconfigContent, clusterName, kubeconfigDir string) error {
	// Create kubeconfig directory if it doesn't exist
	if err := os.MkdirAll(kubeconfigDir, 0o755); err != nil {
		return fmt.Errorf("failed to create kubeconfig directory: %w", err)
	}

	// Build the file path
	filepath := GetKubeconfigPath(clusterName, kubeconfigDir)

	// Write kubeconfig to file with restricted permissions
	if err := os.WriteFile(filepath, []byte(kubeconfigContent), 0o600); err != nil {
		return fmt.Errorf("failed to write kubeconfig file: %w", err)
	}

	log.Info("✓ Kubeconfig saved to: %s", filepath)
	return nil
}

// GetKubeconfigPath returns the path to the kubeconfig file for a given cluster name
// The path is <kubeconfigDir>/<clusterName>.kubeconfig
func GetKubeconfigPath(clusterName, kubeconfigDir string) string {
	filename := fmt.Sprintf("%s.kubeconfig", clusterName)
	return fmt.Sprintf("%s/%s", kubeconfigDir, filename)
}

// RequireKubeconfigFile returns an error if absPath is missing or not a regular file.
// When clusterForMsg is non-empty, not-found errors mention the cluster and suggest
// running dpu-sim deploy.
func RequireKubeconfigFile(absPath, clusterForMsg string) error {
	st, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			if clusterForMsg != "" {
				return fmt.Errorf("kubeconfig for cluster %q not found at %s; did you run dpu-sim deploy?", clusterForMsg, absPath)
			}
			return fmt.Errorf("kubeconfig file not found at %s", absPath)
		}
		return fmt.Errorf("kubeconfig %s: %w", absPath, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("kubeconfig path %s is not a regular file", absPath)
	}
	return nil
}

// FindKubeconfig returns the kubeconfig file path for a cluster if it exists
// Returns the path and nil error if found, or empty string and error if not found
func findKubeconfig(clusterName, kubeconfigDir string) (string, error) {
	filepath := GetKubeconfigPath(clusterName, kubeconfigDir)

	if _, err := os.Stat(filepath); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("kubeconfig for cluster %s not found at %s", clusterName, filepath)
		}
		return "", fmt.Errorf("failed to check kubeconfig file: %w", err)
	}

	return filepath, nil
}

// ReadKubeconfigFile reads and returns the kubeconfig content for a given cluster
func ReadKubeconfigFile(clusterName, kubeconfigDir string) (string, error) {
	filepath, err := findKubeconfig(clusterName, kubeconfigDir)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(filepath)
	if err != nil {
		return "", fmt.Errorf("failed to read kubeconfig file: %w", err)
	}

	return string(content), nil
}

func CleanupKubeconfig(kubeconfigDir string) error {
	// Find all kubeconfig files in the directory
	files, err := filepath.Glob(filepath.Join(kubeconfigDir, "*.kubeconfig"))
	if err != nil {
		return fmt.Errorf("failed to glob kubeconfig files: %w", err)
	}

	// Remove each file individually
	for _, file := range files {
		if err := os.Remove(file); err != nil {
			return fmt.Errorf("failed to remove kubeconfig file %s: %w", file, err)
		}
		log.Info("✓ Kubeconfig file removed: %s", file)
	}

	return nil
}
