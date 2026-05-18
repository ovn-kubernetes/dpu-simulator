package main

import (
	"fmt"
	"strings"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/cni"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/k8s"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
	"github.com/spf13/cobra"
)

var (
	ovnkCluster                string
	ovnkRequireHostCredentials bool
)

var ovnkCmd = &cobra.Command{
	Use:   "ovnk",
	Short: "OVN-Kubernetes support artifacts",
}

var ovnkHostAccessCmd = &cobra.Command{
	Use:   "host-access",
	Short: "Create host-cluster access tokens used by DPU-side components",
	RunE:  runOVNKHostAccess,
}

var ovnkValuesCmd = &cobra.Command{
	Use:   "values",
	Short: "Generate OVN-Kubernetes Helm values and DPU-side FRR artifacts",
	RunE:  runOVNKValues,
}

func init() {
	ovnkHostAccessCmd.Flags().StringVar(&ovnkCluster, "cluster", "", "Host cluster name (default: detected DPU host cluster)")
	ovnkValuesCmd.Flags().StringVar(&ovnkCluster, "cluster", "", "Cluster name to generate values for (default: all OVN-Kubernetes clusters)")
	ovnkValuesCmd.Flags().BoolVar(&ovnkRequireHostCredentials, "require-host-credentials", false, "Fail if DPU host credentials are not available")
	ovnkCmd.AddCommand(ovnkHostAccessCmd)
	ovnkCmd.AddCommand(ovnkValuesCmd)
	rootCmd.AddCommand(ovnkCmd)
}

func runOVNKHostAccess(_ *cobra.Command, args []string) error {
	log.SetLevel(log.ParseLevel(logLevel))
	if len(args) > 0 {
		return fmt.Errorf("unexpected arguments: %v", args)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	clusterName := strings.TrimSpace(ovnkCluster)
	if clusterName == "" {
		clusterName = cfg.GetDPUHostClusterName()
	}
	if clusterName == "" {
		return fmt.Errorf("failed to determine DPU host cluster; pass --cluster")
	}

	kubeconfigPath := k8s.GetKubeconfigPath(clusterName, cfg.Kubernetes.GetKubeconfigDir())
	cniMgr, err := cni.NewCNIManagerWithKubeconfigFile(cfg, kubeconfigPath, platform.NewLocalExecutor())
	if err != nil {
		return fmt.Errorf("failed to create CNI manager: %w", err)
	}

	if err := cniMgr.CreateOVNKubernetesDPUAccessSecret(); err != nil {
		return fmt.Errorf("failed to create OVN-Kubernetes DPU host access: %w", err)
	}
	if err := cniMgr.CreateFRRK8sHostAccess(); err != nil {
		return fmt.Errorf("failed to create FRR-K8S host access: %w", err)
	}

	log.Info("✓ Created host access resources for DPU-side OVN-Kubernetes and FRR-K8S on cluster %s", clusterName)
	return nil
}

func runOVNKValues(_ *cobra.Command, args []string) error {
	log.SetLevel(log.ParseLevel(logLevel))
	if len(args) > 0 {
		return fmt.Errorf("unexpected arguments: %v", args)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	for _, clusterName := range ovnkValuesClusters(cfg, strings.TrimSpace(ovnkCluster)) {
		kubeconfigPath := k8s.GetKubeconfigPath(clusterName, cfg.Kubernetes.GetKubeconfigDir())
		cniMgr, err := cni.NewCNIManagerWithKubeconfigFile(cfg, kubeconfigPath, platform.NewLocalExecutor())
		if err != nil {
			return fmt.Errorf("failed to create CNI manager for cluster %s: %w", clusterName, err)
		}
		if err := cniMgr.GenerateOVNKubernetesHelmValues(clusterName, ovnkRequireHostCredentials); err != nil {
			return fmt.Errorf("failed to generate OVN-Kubernetes values for cluster %s: %w", clusterName, err)
		}
	}

	return nil
}

func ovnkValuesClusters(cfg *config.Config, requested string) []string {
	if requested != "" {
		return []string{requested}
	}

	var clusters []string
	for _, cluster := range cfg.Kubernetes.Clusters {
		if cluster.CNI == config.CNIOVNKubernetes || cfg.DPUClusterNeedsOVNK(cluster.Name) {
			clusters = append(clusters, cluster.Name)
		}
	}
	return clusters
}
