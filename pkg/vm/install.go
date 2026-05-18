package vm

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/cni"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/k8s"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/network"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

// InstallKubernetes installs the software components on a VM
func (m *VMManager) InstallKubernetes(vmName string) error {
	log.Info("=== Installing Kubernetes on VM-based deployment ===")

	k8sMgr := k8s.NewK8sMachineManager(m.config)

	// Install on each VM in the config file
	for _, vmCfg := range m.config.VMs {
		// Install only on specific VM if vmName is set
		if vmName != "" && vmCfg.Name != vmName {
			continue
		}

		// Get VM IP
		mgmtIP, err := m.GetVMMgmtIP(vmCfg.Name)
		if err != nil {
			return fmt.Errorf("failed to get IP for %s: %w", vmCfg.Name, err)
		}

		log.Info("--- Installing Kubernetes on %s (%s) ---", vmCfg.Name, mgmtIP)

		// Get Kubernetes version from config
		k8sVersion := m.config.Kubernetes.Version
		if k8sVersion == "" {
			return fmt.Errorf("kubernetes version is not set")
		}

		cmdExec := platform.NewSSHExecutor(&m.config.SSH, mgmtIP)
		if err := cmdExec.WaitUntilReady(5 * time.Minute); err != nil {
			return fmt.Errorf("failed to wait for SSH on %s: %w", vmCfg.Name, err)
		}

		if err := k8sMgr.InstallKubernetes(cmdExec, vmCfg.Name, k8sVersion); err != nil {
			return fmt.Errorf("failed to install Kubernetes on %s: %w", vmCfg.Name, err)
		}
	}
	return nil
}

func kubeJoinEndpoint(joinCommand string) (string, error) {
	parts := strings.Fields(strings.TrimSpace(joinCommand))
	if len(parts) < 3 || parts[0] != "kubeadm" || parts[1] != "join" {
		return "", fmt.Errorf("unexpected kubeadm join command format")
	}
	return parts[2], nil
}

func rewriteKubeJoinEndpoint(joinCommand, endpoint string) (string, error) {
	parts := strings.Fields(strings.TrimSpace(joinCommand))
	if len(parts) < 3 || parts[0] != "kubeadm" || parts[1] != "join" {
		return "", fmt.Errorf("unexpected kubeadm join command format")
	}
	parts[2] = endpoint
	return strings.Join(parts, " "), nil
}

func endpointReachable(exec platform.CommandExecutor, endpoint string) bool {
	host, port, ok := strings.Cut(endpoint, ":")
	if !ok || host == "" || port == "" {
		return false
	}
	if _, err := strconv.Atoi(port); err != nil {
		return false
	}
	checkCmd := fmt.Sprintf("timeout 4 bash -lc \"cat < /dev/null > /dev/tcp/%s/%s\"", host, port)
	_, _, err := exec.ExecuteWithTimeout(checkCmd, 8*time.Second)
	return err == nil
}

// setupOVNBrExForCluster sets up OVN br-ex on all VMs in the cluster
func (m *VMManager) setupOVNBrExForCluster(clusterRoleMapping config.ClusterRoleMapping, k8sMgr *k8s.K8sMachineManager) error { //nolint:unused
	for role, vms := range clusterRoleMapping {
		for _, vmCfg := range vms {
			vmMgmtIP, err := m.GetVMMgmtIP(vmCfg.Name)
			if err != nil {
				return fmt.Errorf("failed to get mgmt IP for %s: %w", vmCfg.Name, err)
			}

			vmK8sIP, err := m.GetVMK8sIP(vmCfg.Name)
			if err != nil {
				return fmt.Errorf("failed to get K8s IP for %s: %w", vmCfg.Name, err)
			}

			log.Debug("Setting up OVN br-ex on %s (%s) - Mgmt IP: %s, K8s IP: %s",
				vmCfg.Name, role, vmMgmtIP, vmK8sIP)

			exec := platform.NewSSHExecutor(&m.config.SSH, vmMgmtIP)
			if err := k8sMgr.SetupOVNBrEx(exec, vmMgmtIP, vmK8sIP); err != nil {
				return fmt.Errorf("failed to setup OVN br-ex on %s: %w", vmCfg.Name, err)
			}

			k8sMgr.PrintOVNBrExStatus(exec)
		}
	}
	return nil
}

// setupK8sCluster sets up a single Kubernetes cluster
func (m *VMManager) setupK8sCluster(clusterName string, clusterRoleMapping config.ClusterRoleMapping) error {
	k8sMgr := k8s.NewK8sMachineManager(m.config)
	bareMetalRoleMapping := m.config.GetBareMetalClusterRoleMapping()[clusterName]

	clusterCfg := m.config.GetClusterConfig(clusterName)
	if clusterCfg == nil {
		return fmt.Errorf("cluster %s not found in configuration", clusterName)
	}

	// Verify cluster has at least one master node
	masterVMs := clusterRoleMapping[config.ClusterRoleMaster]
	if len(masterVMs) == 0 {
		return fmt.Errorf("no master nodes found for cluster %s", clusterName)
	}
	if len(bareMetalRoleMapping[config.ClusterRoleMaster]) > 0 {
		return fmt.Errorf("baremetal control-plane nodes are not yet supported for cluster %s", clusterName)
	}

	cniType := clusterCfg.CNI
	if cniType == "" {
		return fmt.Errorf("CNI type is not set for cluster %s", clusterName)
	}

	//if cniType == config.CNIOVNKubernetes {
	//	if err := m.setupOVNBrExForCluster(clusterRoleMapping, k8sMgr); err != nil {
	//		return err
	//	}
	//}
	if m.config.DPUClusterNeedsOVNK(clusterCfg.Name) {
		if err := m.setupOVNKubernetesOffloadToDPUOVS(clusterCfg.Name); err != nil {
			return fmt.Errorf("failed to setup OVS on DPU VMs: %w", err)
		}
	}

	// Ensure required OVS bridges exist on all nodes and that ovs-vswitchd
	// has created their kernel datapaths / management sockets. Without this,
	// ovs-ofctl fails with "<bridge> is not a bridge or a socket".
	// This is a weird issue which might point to an issue with ovs-vswitchd,
	// where it doesn't create the management socket for bridges added after
	// startup.
	// TODO: Investigate this further in the future.
	isDPU := m.config.IsOffloadDPU() && m.config.IsDPUCluster(clusterCfg.Name)
	bridges := []string{"br-int"}
	if isDPU {
		gatewayIf := m.config.GatewayInterfaces(clusterCfg.Name)
		bridges = append(bridges, "br"+gatewayIf)
	}
	for _, vms := range clusterRoleMapping {
		for _, vmCfg := range vms {
			mgmtIP, err := m.GetVMMgmtIP(vmCfg.Name)
			if err != nil {
				return fmt.Errorf("failed to get mgmt IP for %s: %w", vmCfg.Name, err)
			}
			exec := platform.NewSSHExecutor(&m.config.SSH, mgmtIP)
			if err := k8sMgr.EnsureOVNBridges(exec, bridges...); err != nil {
				return fmt.Errorf("failed to ensure OVS bridges on %s: %w", vmCfg.Name, err)
			}
		}
	}

	podCIDR := clusterCfg.PodCIDR
	serviceCIDR := clusterCfg.ServiceCIDR

	// Initialize the first master node with kubeadm init
	firstMaster := masterVMs[0]
	firstMasterMgmtIP, err := m.GetVMMgmtIP(firstMaster.Name)
	if err != nil {
		return fmt.Errorf("failed to get mgmt IP for %s: %w", firstMaster.Name, err)
	}
	firstMasterK8sIP, err := m.GetVMK8sIP(firstMaster.Name)
	if err != nil {
		return fmt.Errorf("failed to get K8s IP for %s: %w", firstMaster.Name, err)
	}

	firstMasterExec := platform.NewSSHExecutor(&m.config.SSH, firstMasterMgmtIP)

	log.Info("\n=== Initializing first control plane node: %s ===", firstMaster.Name)
	clusterInfo, err := k8sMgr.InitializeControlPlane(firstMasterExec, firstMaster.Name, firstMasterMgmtIP, podCIDR, serviceCIDR, fmt.Sprintf("%s:6443", firstMasterMgmtIP), []string{firstMasterMgmtIP, firstMasterK8sIP})
	if err != nil {
		return fmt.Errorf("failed to initialize control plane on %s: %w", firstMaster.Name, err)
	}

	if err := k8s.SaveKubeconfigToFile(clusterInfo.Kubeconfig, clusterName, m.config.Kubernetes.KubeconfigDir); err != nil {
		return fmt.Errorf("failed to save kubeconfig for cluster %s: %w", clusterName, err)
	}

	// Join additional master nodes to the control plane
	if len(masterVMs) > 1 {
		log.Info("=== Joining additional control plane nodes ===")
		for _, masterVM := range masterVMs[1:] {
			masterMgmtIP, err := m.GetVMMgmtIP(masterVM.Name)
			if err != nil {
				return fmt.Errorf("failed to get mgmt IP for %s: %w", masterVM.Name, err)
			}

			masterExec := platform.NewSSHExecutor(&m.config.SSH, masterMgmtIP)
			if err := k8sMgr.JoinControlPlane(masterExec, masterVM.Name, clusterInfo); err != nil {
				return fmt.Errorf("failed to join control plane on %s: %w", masterVM.Name, err)
			}
		}
	}

	if err := m.adoptAndJoinBareMetalWorkers(
		k8sMgr,
		bareMetalRoleMapping[config.ClusterRoleWorker],
		clusterInfo,
		firstMasterMgmtIP,
		firstMasterExec,
	); err != nil {
		return err
	}

	workerVMs := clusterRoleMapping[config.ClusterRoleWorker]
	if len(workerVMs) > 0 {
		log.Info("=== Joining worker nodes ===")
		for _, workerVM := range workerVMs {
			workerMgmtIP, err := m.GetVMMgmtIP(workerVM.Name)
			if err != nil {
				return fmt.Errorf("failed to get mgmt IP for %s: %w", workerVM.Name, err)
			}

			workerExec := platform.NewSSHExecutor(&m.config.SSH, workerMgmtIP)
			if err := k8sMgr.JoinWorker(workerExec, workerVM.Name, clusterInfo); err != nil {
				return fmt.Errorf("failed to join worker node %s: %w", workerVM.Name, err)
			}
		}
	}

	log.Info("\n=== Aligning kubelet node-ip with k8s_node_ip (OVN underlay) ===")
	if err := m.ensureKubeletUsesK8sNodeIP(clusterRoleMapping, firstMasterExec); err != nil {
		return fmt.Errorf("kubelet node-ip alignment: %w", err)
	}

	kubeconfigPath := k8s.GetKubeconfigPath(clusterName, m.config.Kubernetes.KubeconfigDir)
	hostLocalExec := platform.NewLocalExecutor()
	cniMgr, err := cni.NewCNIManagerWithKubeconfigFile(m.config, kubeconfigPath, hostLocalExec)
	if err != nil {
		return fmt.Errorf("failed to create CNI manager: %w", err)
	}

	if err := cniMgr.InstallCNI(cniType, clusterName, firstMasterK8sIP); err != nil {
		return fmt.Errorf("failed to install CNI: %w", err)
	}

	if err := cniMgr.InstallAddons(clusterCfg.Addons, clusterName, firstMasterK8sIP); err != nil {
		return fmt.Errorf("failed to install addons: %w", err)
	}

	if err := cniMgr.PostInstallPerCluster(clusterName); err != nil {
		return fmt.Errorf("failed to patch cluster environment: %w", err)
	}
	log.Info("✓ Kubernetes cluster %s setup complete", clusterName)
	return nil
}

// ensureKubeletUsesK8sNodeIP sets kubelet --node-ip to each VM's k8s_node_ip so Node.Status and
// OVN use the k8s L3 subnet. The primary master is patched last to shorten API disruption from
// kubelet restarting static control-plane pods.
func (m *VMManager) ensureKubeletUsesK8sNodeIP(clusterRoleMapping config.ClusterRoleMapping, firstMasterExec platform.CommandExecutor) error {
	masters := clusterRoleMapping[config.ClusterRoleMaster]
	var patchOrder []config.VMConfig
	patchOrder = append(patchOrder, clusterRoleMapping[config.ClusterRoleWorker]...)
	if len(masters) > 1 {
		patchOrder = append(patchOrder, masters[1:]...)
	}
	if len(masters) > 0 {
		patchOrder = append(patchOrder, masters[0])
	}
	if len(patchOrder) == 0 {
		return nil
	}
	for _, vmCfg := range patchOrder {
		ip := strings.TrimSpace(vmCfg.K8sNodeIP)
		if ip == "" {
			continue
		}
		mgmtIP, err := m.GetVMMgmtIP(vmCfg.Name)
		if err != nil {
			return fmt.Errorf("mgmt IP for %s: %w", vmCfg.Name, err)
		}
		exec := platform.NewSSHExecutor(&m.config.SSH, mgmtIP)
		if err := k8s.EnsureKubeletK8sNodeIP(exec, ip); err != nil {
			return fmt.Errorf("%s: %w", vmCfg.Name, err)
		}
	}
	if err := k8s.WaitAllNodesReady(firstMasterExec, 6*time.Minute); err != nil {
		return err
	}
	return nil
}

// adoptAndJoinBareMetalWorkers prepares baremetal worker nodes and joins them
// to an initialized cluster.
//
// It handles bootstrap SSH access, optional bootc reconcile/reboot, node reset,
// Kubernetes installation, hybrid-network preparation, and join endpoint
// fallback when the default control-plane endpoint is not reachable.
func (m *VMManager) adoptAndJoinBareMetalWorkers(k8sMgr *k8s.K8sMachineManager, bareMetalWorkers []config.BareMetalConfig, clusterInfo *k8s.ControlPlaneInfo, firstMasterMgmtIP string, firstMasterExec platform.CommandExecutor) error {
	if len(bareMetalWorkers) == 0 {
		return nil
	}

	log.Info("=== Adopting and joining baremetal worker nodes ===")
	defaultJoinEndpoint, err := kubeJoinEndpoint(clusterInfo.WorkerJoinCommand)
	if err != nil {
		return fmt.Errorf("failed to parse worker join command endpoint: %w", err)
	}
	fallbackJoinEndpoint := fmt.Sprintf("%s:6443", firstMasterMgmtIP)

	for _, node := range bareMetalWorkers {
		workerExec, err := m.ensureBareMetalSSHAccess(node)
		if err != nil {
			return fmt.Errorf("failed to establish SSH access to baremetal node %s: %w", node.Name, err)
		}

		if err := m.maybeApplyBootc(node, workerExec); err != nil {
			return fmt.Errorf("failed bootc reconcile on baremetal node %s: %w", node.Name, err)
		}

		// Recreate executor in case bootc rebooted and the previous SSH session is stale.
		workerExec = m.globalSSHExecutor(node.MgmtIP)
		if err := workerExec.WaitUntilReady(5 * time.Minute); err != nil {
			return fmt.Errorf("baremetal node %s not reachable after bootc processing: %w", node.Name, err)
		}

		if err := m.resetBareMetalNode(node, workerExec); err != nil {
			return fmt.Errorf("failed to reset baremetal node %s: %w", node.Name, err)
		}

		if err := k8sMgr.InstallKubernetes(workerExec, node.Name, m.config.Kubernetes.Version); err != nil {
			return fmt.Errorf("failed to install Kubernetes on baremetal node %s: %w", node.Name, err)
		}
		if err := k8sMgr.EnsureOVNBrInt(workerExec); err != nil {
			return fmt.Errorf("failed to ensure br-int on baremetal node %s: %w", node.Name, err)
		}
		if err := m.setKubeletNodeIP(node, workerExec); err != nil {
			return fmt.Errorf("failed to set kubelet node-ip on baremetal node %s: %w", node.Name, err)
		}
		if err := m.ensureHybridNetworking(node, firstMasterExec); err != nil {
			return fmt.Errorf("failed to prepare hybrid networking for baremetal node %s: %w", node.Name, err)
		}

		bareMetalJoinInfo := *clusterInfo
		chosenJoinEndpoint := defaultJoinEndpoint
		if !endpointReachable(workerExec, defaultJoinEndpoint) {
			if endpointReachable(workerExec, fallbackJoinEndpoint) {
				chosenJoinEndpoint = fallbackJoinEndpoint
			} else {
				return fmt.Errorf("baremetal node %s cannot reach kubeadm join endpoints %s or %s", node.Name, defaultJoinEndpoint, fallbackJoinEndpoint)
			}
		}

		if chosenJoinEndpoint != defaultJoinEndpoint {
			rewrittenJoinCmd, err := rewriteKubeJoinEndpoint(clusterInfo.WorkerJoinCommand, chosenJoinEndpoint)
			if err != nil {
				return fmt.Errorf("failed to rewrite worker join command endpoint: %w", err)
			}
			bareMetalJoinInfo.WorkerJoinCommand = rewrittenJoinCmd
			log.Warn("Baremetal node %s cannot reach default join endpoint %s; falling back to %s", node.Name, defaultJoinEndpoint, chosenJoinEndpoint)
		}

		if err := k8sMgr.JoinWorker(workerExec, node.Name, &bareMetalJoinInfo); err != nil {
			return fmt.Errorf("failed to join baremetal worker node %s: %w", node.Name, err)
		}
	}

	return nil
}

// setupOVNKubernetesOffloadToDPUOVS configures OVS external_ids on all DPU VMs in the given
// cluster. OVS is already installed on VMs during InstallKubernetes; this
// sets the external_ids that ovnkube-node DPU mode needs.
func (m *VMManager) setupOVNKubernetesOffloadToDPUOVS(dpuClusterName string) error {
	pairs := m.config.GetHostDPUPairs(dpuClusterName)
	if len(pairs) == 0 {
		return nil
	}

	for _, pair := range pairs {
		mgmtIP, err := m.GetVMMgmtIP(pair.DPUNode)
		if err != nil {
			return fmt.Errorf("failed to get mgmt IP for DPU %s: %w", pair.DPUNode, err)
		}

		encapIP, err := m.GetVMK8sIP(pair.DPUNode)
		if err != nil {
			return fmt.Errorf("failed to get K8s IP for DPU %s: %w", pair.DPUNode, err)
		}

		sshExec := platform.NewSSHExecutor(&m.config.SSH, mgmtIP)
		if err := cni.SetupOVNKOffloadToDPUNodeOVS(sshExec, pair.DPUNode, pair.HostNode, encapIP); err != nil {
			return err
		}
	}
	return nil
}

// SetupAllK8sClusters sets up all Kubernetes clusters from the configuration.
// Clusters are processed in install order (host clusters before DPU clusters
// when offloading is enabled).
func (m *VMManager) SetupAllK8sClusters() error {
	clusterRoleMapping := m.config.GetClusterRoleMapping()
	for _, clusterCfg := range m.config.ClustersOrderedForInstall() {
		log.Info("\n=== Setting up Kubernetes cluster %s ===", clusterCfg.Name)
		if err := m.setupK8sCluster(clusterCfg.Name, clusterRoleMapping[clusterCfg.Name]); err != nil {
			return fmt.Errorf("failed to setup Kubernetes cluster %s: %w", clusterCfg.Name, err)
		}
	}
	if err := cni.PostInstall(m.config, platform.NewLocalExecutor()); err != nil {
		return fmt.Errorf("failed CNI post-install after all clusters: %w", err)
	}
	return nil
}

// AssignDpuHostGatewayIPs SSHes into each DPU Host VM and assigns a gateway
// IP to eth0-0 so OVN-Kubernetes DPU Host mode can find an IPv4 address on the
// gateway interface. IPs are allocated from the top of the k8s subnet, skipping
// all IPs already used by VMs or the network gateway.
func (m *VMManager) AssignDpuHostGatewayIPs() error {
	if !m.config.IsOffloadDPU() {
		return nil
	}

	k8sNet := m.config.GetNetworkByType(config.K8sNetworkName)
	if k8sNet == nil || k8sNet.SubnetMask == "" {
		return nil
	}

	prefix, err := config.PrefixLenFromSubnetMask(k8sNet.SubnetMask)
	if err != nil {
		return err
	}

	_, subnet, err := net.ParseCIDR(fmt.Sprintf("%s/%d", k8sNet.Gateway, prefix))
	if err != nil {
		return err
	}

	var usedIPs []net.IP
	if gw := net.ParseIP(k8sNet.Gateway); gw != nil {
		usedIPs = append(usedIPs, gw)
	}
	for _, vm := range m.config.VMs {
		if ip := net.ParseIP(vm.K8sNodeIP); ip != nil {
			usedIPs = append(usedIPs, ip)
		}
	}

	gwIf := fmt.Sprintf(network.HostDataIfFmt, 0)

	for _, pair := range m.config.GetHostDPUPairs("") {
		gwIP, err := network.GetFreeIPv4AddressInSubnet(subnet, usedIPs)
		if err != nil {
			return fmt.Errorf("no free IP for %s: %w", pair.HostNode, err)
		}
		usedIPs = append(usedIPs, gwIP)

		gwCIDR := fmt.Sprintf("%s/%d", gwIP, prefix)

		mgmtIP, err := m.GetVMMgmtIP(pair.HostNode)
		if err != nil {
			return fmt.Errorf("failed to get mgmt IP for %s: %w", pair.HostNode, err)
		}

		sshExec := platform.NewSSHExecutor(&m.config.SSH, mgmtIP)

		// The virtio interface may not be renamed by udev yet; poll until it appears.
		if _, _, err := sshExec.ExecuteRetryWithTimeout(
			fmt.Sprintf("ip link show %s", gwIf), 2*time.Second, 60*time.Second,
		); err != nil {
			return fmt.Errorf("interface %s not ready on %s: %w", gwIf, pair.HostNode, err)
		}

		if err := sshExec.RunCmd(log.LevelDebug, "ip", "addr", "add", gwCIDR, "dev", gwIf, "noprefixroute"); err != nil {
			return fmt.Errorf("failed to assign %s to %s on %s: %w", gwCIDR, gwIf, pair.HostNode, err)
		}

		log.Info("Assigned %s to %s on %s", gwCIDR, gwIf, pair.HostNode)
	}

	return nil
}
