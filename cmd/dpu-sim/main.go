package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/cni"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/containerengine"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/deviceplugin"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/k8s"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/kind"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/registry"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/requirements"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/vm"
	"github.com/spf13/cobra"
)

var (
	// Global flags
	configPath        string
	logLevel          string
	ovnKubernetesPath string
	ovnKubernetesMode string

	// Root command flags
	skipDeps    bool
	skipCleanup bool
	cleanupOnly bool
	skipDeploy  bool
	skipK8s     bool
	rebuildCNI  bool
	redeployCNI bool
)

var rootCmd = &cobra.Command{
	Use:   "dpu-sim",
	Short: "DPU Simulator - Complete setup orchestration",
	Long: `DPU Simulator automates deployment of DPU simulation environments
using either VMs (libvirt) or containers (Kind), pre-configured with
Kubernetes and CNI for container networking experiments.

This is the main orchestrator that runs the complete deployment workflow:
  1. Install dependencies
  2. Clean up existing resources (Idempotent deployment - can be run multiple times safely)
  3. Deploy infrastructure (VMs or Kind clusters)
  4. Install Kubernetes and CNI components`,
	RunE: runDeploy,
}

func init() {
	// Global persistent flag
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "config.yaml", "Path to configuration file")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info",
		fmt.Sprintf("Log level (%s)", strings.Join(log.ValidLevels(), ", ")))
	rootCmd.PersistentFlags().StringVar(&ovnKubernetesPath, "ovn-kubernetes-path", "",
		"Path to an external ovn-kubernetes source tree (overrides auto-clone)")
	rootCmd.PersistentFlags().StringVar(&ovnKubernetesMode, "ovnk-mode", config.OVNKubernetesModeInstall,
		fmt.Sprintf("OVN-Kubernetes handling mode (%s|%s)", config.OVNKubernetesModeInstall, config.OVNKubernetesModeValuesOnly))

	// Root command flags
	rootCmd.Flags().BoolVar(&cleanupOnly, "cleanup", false, "Only cleanup existing resources, do not deploy")
	rootCmd.Flags().BoolVar(&skipDeps, "skip-deps", false, "Skip dependency checks")
	rootCmd.Flags().BoolVar(&skipCleanup, "skip-cleanup", false, "Skip cleanup of existing resources")
	rootCmd.Flags().BoolVar(&skipDeploy, "skip-deploy", false, "Skip VM/Kind deployment")
	rootCmd.Flags().BoolVar(&skipK8s, "skip-k8s", false, "Skip Kubernetes (VM only) and CNI installation")
	rootCmd.Flags().BoolVar(&rebuildCNI, "rebuild-cni", false, "Rebuild the OVN-Kubernetes CNI image and exit")
	rootCmd.Flags().BoolVar(&redeployCNI, "redeploy-cni", false, "Redeploy the OVN-Kubernetes CNI image onto each cluster and exit")
}

// =============================================================================
// Root command - Orchestrated deployment
// =============================================================================

func runDeploy(cmd *cobra.Command, args []string) error {
	// Configure log level
	log.SetLevel(log.ParseLevel(logLevel))

	log.Info("╔═══════════════════════════════════════════════╗")
	log.Info("║               DPU Simulator                   ║")
	log.Info("╚═══════════════════════════════════════════════╝")
	log.Info("Configuration: %s", configPath)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	ovnPath := strings.TrimSpace(ovnKubernetesPath)
	if err := cni.ValidateOVNKubernetesPath(ovnPath); err != nil {
		return fmt.Errorf("--ovn-kubernetes-path: %w", err)
	}
	cfg.OVNKubernetesPath = ovnPath
	if err := cfg.SetOVNKubernetesMode(strings.TrimSpace(ovnKubernetesMode)); err != nil {
		return err
	}

	// Create registry manager once if configured; nil otherwise.
	localExec := platform.NewLocalExecutor()
	engine, err := containerengine.NewProjectEngine(localExec)
	if err != nil {
		return fmt.Errorf("failed to select container engine: %w", err)
	}

	var regMgr *registry.RegistryManager = nil
	var buildCNIImage registry.BuildFunc
	if cfg.IsRegistryEnabled() {
		regMgr = registry.NewRegistryManagerWithRuntime(cfg, localExec, engine)
		buildCNIImage = cni.BuildCNIImageWithRuntime(cfg, localExec, engine)
	}

	// Handle rebuild CNI image(s) and redeploy onto each cluster
	if rebuildCNI || redeployCNI {
		if !cfg.IsRegistryEnabled() {
			return fmt.Errorf("--rebuild-cni/--redeploy-cni requires registry.enabled=true")
		}
		if !cfg.HasRegistry() {
			return fmt.Errorf("--rebuild-cni/--redeploy-cni requires registry.containers with at least one CNI image entry")
		}

		log.Info("\n=== Rebuilding CNI images ===")
		if err := regMgr.SetupAll(buildCNIImage); err != nil {
			return fmt.Errorf("failed to rebuild CNI images: %w", err)
		}
		if cfg.IsOffloadDPU() {
			if _, err := deviceplugin.BuildAndLoadImage(localExec, engine, regMgr); err != nil {
				return fmt.Errorf("failed to build/push device plugin image: %w", err)
			}
		}

		if redeployCNI {
			log.Info("\n=== Redeploying CNI images ===")
			cniMgr, err := cni.NewCNIManager(cfg, localExec)
			if err != nil {
				return fmt.Errorf("failed to create CNI manager: %w", err)
			}
			kubeconfigDir := cfg.Kubernetes.GetKubeconfigDir()
			for _, cluster := range cfg.Kubernetes.Clusters {
				kubeconfigPath := k8s.GetKubeconfigPath(cluster.Name, kubeconfigDir)
				if err := cniMgr.SetKubeconfigFile(kubeconfigPath); err != nil {
					return fmt.Errorf("failed to set kubeconfig for cluster %s: %w", cluster.Name, err)
				}
				if err := cniMgr.RedeployCNI(cluster.Name); err != nil {
					return fmt.Errorf("failed to redeploy CNI on cluster %s: %w", cluster.Name, err)
				}
			}
		}

		return nil
	}

	deployMode, err := cfg.GetDeploymentMode()
	if err != nil {
		return fmt.Errorf("failed to determine deployment mode: %w", err)
	}
	log.Info("Deployment mode: %s", deployMode)

	if !skipDeps {
		log.Info("\n=== Checking Dependencies ===")
		if err := requirements.EnsureDependencies(cfg); err != nil {
			return fmt.Errorf("dependency check failed: %w", err)
		}
	} else {
		log.Info("\nSkipping dependency check")
	}

	if !skipCleanup || cleanupOnly {
		log.Info("\n=== Cleaning up K8s ===")
		if err := k8s.CleanupAll(cfg); err != nil {
			log.Warn("Warning: Kubernetes cleanup failed: %v", err)
		}
	} else {
		log.Info("\nSkipping Kubernetes cleanup")
	}

	// Start local registry and build/push images if configured.
	// Skip this entirely for cleanup-only runs.
	if regMgr != nil && !cleanupOnly {
		if err := regMgr.SetupAll(buildCNIImage); err != nil {
			return fmt.Errorf("registry setup failed: %w", err)
		}
		if cfg.IsOffloadDPU() {
			if _, err := deviceplugin.BuildAndLoadImage(localExec, engine, regMgr); err != nil {
				return fmt.Errorf("failed to build/push device plugin image: %w", err)
			}
		}
	}

	switch deployMode {
	case config.VMDeploymentMode:
		return runVMDeploymentWorkflow(cfg, regMgr, localExec)
	case config.KindDeploymentMode:
		return runKindDeploymentWorkflow(cfg, regMgr, localExec, engine)
	default:
		return fmt.Errorf("unknown deployment mode: %s", deployMode)
	}
}

func runVMDeploymentWorkflow(cfg *config.Config, regMgr *registry.RegistryManager, hostExec platform.CommandExecutor) error {
	log.Info("")
	log.Info("╔═══════════════════════════════════════════════╗")
	log.Info("║       VM-Based Deployment Workflow            ║")
	log.Info("╚═══════════════════════════════════════════════╝")

	if err := vm.EnsureHostNetworkPrerequisites(hostExec); err != nil {
		return fmt.Errorf("failed to prepare host network prerequisites: %w", err)
	}

	vmMgr, err := vm.NewVMManager(cfg, hostExec)
	if err != nil {
		return fmt.Errorf("failed to create VM manager: %w", err)
	}
	defer vmMgr.Close()

	if !skipCleanup || cleanupOnly {
		if err := vmMgr.CleanupAll(); err != nil {
			log.Warn("Warning: cleanup failed: %v", err)
		}
		if cleanupOnly {
			if regMgr != nil {
				if err := regMgr.Stop(); err != nil {
					log.Warn("Warning: registry cleanup failed: %v", err)
				}
			}
			log.Info("\n✓ Cleanup complete. No deployment performed.")
			return nil
		}
	}

	if !skipDeploy {
		log.Info("\n=== Deploying VMs ===")
		if err := doVMDeploy(cfg, vmMgr); err != nil {
			return fmt.Errorf("VM deployment failed: %w", err)
		}
	} else {
		log.Info("\nSkipping VM deployment")
	}

	if !skipK8s {
		log.Info("\n=== Installing Kubernetes and CNI ===")
		if err := doVMInstallK8s(vmMgr); err != nil {
			return fmt.Errorf("kubernetes installation failed: %w", err)
		}
	} else {
		log.Info("\nSkipping Kubernetes installation")
	}

	printSuccessMessage(cfg, "VM")
	return nil
}

func runKindDeploymentWorkflow(cfg *config.Config, regMgr *registry.RegistryManager, hostExec platform.CommandExecutor, engine containerengine.Engine) error {
	log.Info("")
	log.Info("╔═══════════════════════════════════════════════╗")
	log.Info("║      Kind-Based Deployment Workflow           ║")
	log.Info("╚═══════════════════════════════════════════════╝")

	kindMgr, err := kind.NewKindManager(cfg)
	if err != nil {
		return fmt.Errorf("kind manager: %w", err)
	}

	if !skipCleanup || cleanupOnly {
		log.Info("\n=== Cleaning up existing kind clusters ===")
		if err := kindMgr.CleanupAll(cfg); err != nil {
			return fmt.Errorf("failed to cleanup Kind clusters: %w", err)
		}
		if cleanupOnly {
			if regMgr != nil {
				if err := regMgr.Stop(); err != nil {
					log.Warn("Warning: registry cleanup failed: %v", err)
				}
			}
			log.Info("✓ Cleanup complete")
			return nil
		}
	}

	if !skipDeploy {
		log.Info("\n=== Ensuring Kind host prerequisites ===")
		if err := kindMgr.InstallHostDependencies(hostExec); err != nil {
			return fmt.Errorf("failed to configure Kind host prerequisites: %w", err)
		}

		log.Info("\n=== Deploying Kind clusters ===")
		if err := doKindDeploy(cfg, kindMgr, regMgr, hostExec); err != nil {
			return fmt.Errorf("kind deployment failed: %w", err)
		}
	} else {
		log.Info("\nSkipping Kind deployment")
	}

	if !skipK8s {
		log.Info("\n=== Installing CNI ===")
		if err := buildAndLoadKindRegistryImages(cfg, kindMgr, hostExec, engine); err != nil {
			return fmt.Errorf("failed to build/load registry images into Kind: %w", err)
		}
		if err := doKindInstallCNI(kindMgr, hostExec); err != nil {
			return fmt.Errorf("CNI installation failed: %w", err)
		}
	} else {
		log.Info("\nSkipping CNI installation")
	}

	printSuccessMessage(cfg, "Kind")
	return nil
}

func printSuccessMessage(cfg *config.Config, deployType string) {
	log.Info("")
	log.Info("╔═══════════════════════════════════════════════╗")
	log.Info("║         Deployment Completed Successfully!    ║")
	log.Info("╚═══════════════════════════════════════════════╝")

	if deployType == "VM" {
		log.Info("\n✓ VM deployment complete!")
		log.Info("\nYour DPU simulation environment is ready:")
		log.Info("  • VMs are running and accessible")
		log.Info("  • Kubernetes is installed and configured")
		log.Info("  • CNI is deployed and ready")
		log.Info("\nUseful commands:")
		log.Info("  vmctl list                    # List all VMs")
		log.Info("  vmctl ssh <vm-name>           # SSH into a VM")
	} else {
		log.Info("\n✓ Kind deployment complete!")
		log.Info("\nYour DPU simulation environment is ready:")
		log.Info("  • Kind clusters are running")
		log.Info("  • CNI is deployed and ready")
		log.Info("\nUseful commands:")
		log.Info("  kind get clusters             # List all clusters")
	}
	kubeconfigDir := cfg.Kubernetes.GetKubeconfigDir()
	for _, c := range cfg.Kubernetes.Clusters {
		log.Info("  kubectl --kubeconfig %s get nodes", k8s.GetKubeconfigPath(c.Name, kubeconfigDir))
	}

	log.Info("\nKubeconfig directory: %s", cfg.Kubernetes.GetKubeconfigDir())
	log.Info("For more information, see README.md")
}

func doVMDeploy(cfg *config.Config, vmMgr *vm.VMManager) error {
	// Create networks
	if err := vmMgr.CreateAllNetworks(); err != nil {
		return fmt.Errorf("failed to create networks: %w", err)
	}

	// Create VMs
	if err := vmMgr.CreateAllVMs(); err != nil {
		return fmt.Errorf("failed to create VMs: %w", err)
	}

	// Wait for VMs to get IP addresses
	log.Info("\n=== Waiting for VMs to boot and get IPs ===")
	for _, vmCfg := range cfg.VMs {
		log.Info("Waiting for %s to get an IP address...", vmCfg.Name)
		ip, err := vmMgr.WaitForVMIP(vmCfg.Name, config.MgmtNetworkName, 5*time.Minute)
		if err != nil {
			return fmt.Errorf("failed to get IP for %s: %w", vmCfg.Name, err)
		}
		log.Info("✓ %s IP: %s", vmCfg.Name, ip)

		cmdExec := platform.NewSSHExecutor(&cfg.SSH, ip)
		log.Info("Waiting for SSH on %s...", vmCfg.Name)
		if err := cmdExec.WaitUntilReady(5 * time.Minute); err != nil {
			return fmt.Errorf("failed to wait for SSH on %s: %w", vmCfg.Name, err)
		}
		log.Info("✓ SSH ready on %s, waiting for cloud-init to finish...", vmCfg.Name)
		// Exit code 0 = success, 2 = done with recoverable errors; both mean cloud-init finished.
		stdout, _, _ := cmdExec.Execute("cloud-init status --wait")
		log.Info("✓ cloud-init finished on %s (%s)", vmCfg.Name, strings.TrimSpace(stdout))
	}

	// We don't need to setup host-to-DPU virtio pairs because it is done at VM creation time
	// Assign DPU Host gateway IPs to eth0-0 for DPU Host mode
	if err := vmMgr.AssignDpuHostGatewayIPs(); err != nil {
		return fmt.Errorf("failed to assign DPU Host gateway IPs: %w", err)
	}

	return nil
}

func doVMInstallK8s(vmMgr *vm.VMManager) error {
	if err := vmMgr.InstallKubernetes(""); err != nil {
		return fmt.Errorf("failed to install Kubernetes: %w", err)
	}

	if err := vmMgr.SetupAllK8sClusters(); err != nil {
		return fmt.Errorf("failed to setup Kubernetes clusters: %w", err)
	}

	return nil
}

func buildAndLoadKindRegistryImages(cfg *config.Config, kindMgr *kind.KindManager, hostExec platform.CommandExecutor, engine containerengine.Engine) error {
	if !(cfg.IsRegistryImageBuildOnly() && cfg.IsKindMode()) {
		return nil
	}
	if err := kindMgr.BuildAndLoadImagesFromRegistryConfig(hostExec, engine); err != nil {
		return fmt.Errorf("failed to build/load registry images into Kind: %w", err)
	}
	return nil
}

func doKindDeploy(cfg *config.Config, kindMgr *kind.KindManager, regMgr *registry.RegistryManager, hostExec platform.CommandExecutor) error {
	log.Info("\n=== Creating Kind Clusters ===")
	if err := kindMgr.DeployAllClusters(); err != nil {
		return fmt.Errorf("failed to deploy Kind clusters: %w", err)
	}

	// Connect the local registry to the Kind Docker network and resolve its
	// IP so containerd on each node can reach it without DNS.
	var registryIP string
	if regMgr != nil {
		if err := regMgr.ConnectToKindNetwork(); err != nil {
			return fmt.Errorf("failed to connect registry to kind network: %w", err)
		}
		ip, err := regMgr.GetKindNetworkIP()
		if err != nil {
			return fmt.Errorf("failed to get registry IP on kind network: %w", err)
		}
		registryIP = ip
		log.Info("Registry IP on kind network: %s", registryIP)
	}

	for _, cluster := range cfg.Kubernetes.Clusters {
		info, err := kindMgr.GetClusterInfo(hostExec, cluster.Name)
		if err != nil {
			log.Warn("Warning: failed to get info for %s: %v", cluster.Name, err)
			continue
		}

		log.Info("\nCluster: %s", info.Name)
		log.Info("  Status: %s", info.Status)
		log.Info("  Nodes:")
		for _, node := range info.Nodes {
			log.Info("    - %s (%s) [%s]", node.Name, node.Role, node.Status)
		}

		for _, node := range info.Nodes {
			dockerExec := platform.NewDockerExecutor(node.Name, kindMgr.ContainerBin())
			if err := kindMgr.InstallDependencies(dockerExec); err != nil {
				return fmt.Errorf("failed to install Kind dependencies on %s: %w", node.Name, err)
			}
			if regMgr != nil {
				if err := kindMgr.ConfigureRegistryOnNode(dockerExec, registryIP); err != nil {
					return fmt.Errorf("failed to configure registry on %s: %w", node.Name, err)
				}
			}
		}
	}

	// Setup host-to-DPU veth topology (pairs can span host and DPU clusters)
	if err := kindMgr.SetupHostToDpuNetwork(); err != nil {
		return fmt.Errorf("failed to setup host-to-DPU veth topology: %w", err)
	}

	return nil
}

func doKindInstallCNI(kindMgr *kind.KindManager, cmdExec platform.CommandExecutor) error {
	log.Info("\n=== Installing CNI on Kind clusters ===")

	if err := kindMgr.InstallCNI(cmdExec); err != nil {
		return fmt.Errorf("failed to deploy CNI: %w", err)
	}
	return nil
}

func main() {
	start := time.Now()
	err := rootCmd.Execute()
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "dpu-sim total time: %s\n", elapsed)
	if err != nil {
		os.Exit(1)
	}
}
