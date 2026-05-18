package cni

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/deviceplugin"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/k8s"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

// PostInstallPerCluster applies cluster-environment patches after the CNI, device plugin
// (when applicable), and addons are installed on this cluster. For example, in
// OVN-Kubernetes DPU-host mode it patches system Deployments so workloads request the simulated VF.
// It then scales CoreDNS and local-path-provisioner to zero; PostInstall restores them.
func (m *CNIManager) PostInstallPerCluster(clusterName string) error {
	mode := m.detectOVNKMode(clusterName)

	// During Cluster installation, we suspend CoreDNS and local-path-provisioner
	// Because certain components may not be ready (such as OVN-K in DPU Host and DPU modes)
	// TODO: This should be fixed in such a way that we can recover.
	m.suspendCoreDNSAndLocalPathProvisioner()

	if mode == ovnkModeDPUHost {
		log.Info("\n=== Patching cluster environment on %s ===", clusterName)
		if err := m.ensureDPUHostSystemDeployments(); err != nil {
			return fmt.Errorf("cluster environment patch: %w", err)
		}
	}

	return nil
}

// PostInstall runs after every configured cluster has been installed successfully.
// It restores CoreDNS and local-path-provisioner replica counts, then rollout-restarts
// them so new pods pick up stable CNI wiring.
// cmdExec is required and forwarded to each cluster's CNIManager (use the same executor as CNI install when applicable).
func PostInstall(cfg *config.Config, cmdExec platform.CommandExecutor) error {
	if cfg == nil {
		return fmt.Errorf("cni post-install: config is nil")
	}
	if cmdExec == nil {
		return fmt.Errorf("cni post-install: command executor is nil")
	}

	log.Info("\n=== Post-install (all clusters): restoring CoreDNS / local-path-provisioner and rolling out ===")
	for _, clusterCfg := range cfg.ClustersOrderedForInstall() {
		if deferSystemDeploymentsUntilExternalOVNK(cfg, clusterCfg.Name) {
			log.Info("Leaving CoreDNS / local-path-provisioner suspended on cluster %s until external OVN-Kubernetes install completes", clusterCfg.Name)
			continue
		}

		kubeconfigPath := k8s.GetKubeconfigPath(clusterCfg.Name, cfg.Kubernetes.GetKubeconfigDir())
		cniMgr, err := NewCNIManagerWithKubeconfigFile(cfg, kubeconfigPath, cmdExec)
		if err != nil {
			return fmt.Errorf("cni post-install for cluster %s: %w", clusterCfg.Name, err)
		}
		log.Info("Resuming system deployments on cluster %s", clusterCfg.Name)
		cniMgr.resumeCoreDNSAndLocalPathProvisioner()
		cniMgr.rolloutRestartCoreDNSAndLocalPathProvisioner()
	}

	return nil
}

func deferSystemDeploymentsUntilExternalOVNK(cfg *config.Config, clusterName string) bool {
	if cfg.ShouldInstallOVNKubernetes() {
		return false
	}
	return cfg.GetCNIType(clusterName) == config.CNIOVNKubernetes || cfg.DPUClusterNeedsOVNK(clusterName)
}

// suspendCoreDNSAndLocalPathProvisioner scales CoreDNS and local-path-provisioner
// to zero
func (m *CNIManager) suspendCoreDNSAndLocalPathProvisioner() {
	if err := m.k8sClient.SuspendDeployment("kube-system", "coredns", false); err != nil {
		log.Warn("failed to suspend coredns: %v", err)
	}
	if err := m.k8sClient.SuspendDeployment("local-path-storage", "local-path-provisioner", true); err != nil {
		log.Warn("failed to suspend local-path-provisioner: %v", err)
	}
}

// resumeCoreDNSAndLocalPathProvisioner scales CoreDNS and local-path-provisioner
// back to the original replica count
func (m *CNIManager) resumeCoreDNSAndLocalPathProvisioner() {
	if err := m.k8sClient.ResumeDeployment("kube-system", "coredns", false); err != nil {
		log.Warn("failed to resume coredns: %v", err)
	}
	if err := m.k8sClient.ResumeDeployment("local-path-storage", "local-path-provisioner", true); err != nil {
		log.Warn("failed to resume local-path-provisioner: %v", err)
	}
}

// rolloutRestartCoreDNSAndLocalPathProvisioner restarts CoreDNS and local-path-provisioner by bumping
// the pod template restartedAt annotation.
func (m *CNIManager) rolloutRestartCoreDNSAndLocalPathProvisioner() {
	if err := m.k8sClient.RolloutRestartDeployment("kube-system", "coredns", false); err != nil {
		log.Warn("failed to restart coredns: %v", err)
	}

	if err := m.k8sClient.RolloutRestartDeployment("local-path-storage", "local-path-provisioner", true); err != nil {
		log.Warn("failed to restart local-path-provisioner: %v", err)
	}
}

// ensureDPUHostSystemDeployments patches CoreDNS and (if present)
// local-path-provisioner so pods request the simulated VF and schedule only on
// dpu-host nodes. Without this, DPU-host CNI rejects pods with "device ID must be provided".
// TODO: This is considered a workaround for now.
func (m *CNIManager) ensureDPUHostSystemDeployments() error {
	vf := deviceplugin.VFResourceName
	if err := patchDeploymentForDPUHostPodNetworking(m.k8sClient, "kube-system", "coredns", vf, false); err != nil {
		return err
	}
	if err := patchDeploymentForDPUHostPodNetworking(m.k8sClient, "local-path-storage", "local-path-provisioner", vf, true); err != nil {
		return err
	}
	return nil
}

// PatchDeploymentForDPUHostPodNetworking adds vfResourceName (e.g. dpusim.io/vf) to
// requests and limits for every container and init container in the pod template, and
// requires scheduling onto nodes labeled DPU Host.
// ignoreNotFound treats a missing Deployment as success (for optional add-ons).
func patchDeploymentForDPUHostPodNetworking(c *k8s.K8sClient, namespace, deploymentName, vfResourceName string, ignoreNotFound bool) error {
	qty := resource.MustParse("1")
	rn := corev1.ResourceName(vfResourceName)

	var skippedNotFound bool
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		dep, err := c.Clientset().AppsV1().Deployments(namespace).Get(ctx, deploymentName, metav1.GetOptions{})
		if err != nil {
			if ignoreNotFound && apierrors.IsNotFound(err) {
				log.Info("Deployment %s/%s not found, skipping DPU-host simulated VF patch", namespace, deploymentName)
				skippedNotFound = true
				return nil
			}
			return fmt.Errorf("get deployment %s/%s: %w", namespace, deploymentName, err)
		}

		pod := &dep.Spec.Template.Spec
		for i := range pod.InitContainers {
			patchContainerVFRequest(&pod.InitContainers[i], rn, qty)
		}
		for i := range pod.Containers {
			patchContainerVFRequest(&pod.Containers[i], rn, qty)
		}
		mergeDPUHostNodeAffinity(pod)

		if _, err := c.Clientset().AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update deployment %s/%s for DPU-host simulated VF: %w", namespace, deploymentName, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if skippedNotFound {
		return nil
	}

	log.Info("✓ Patched deployment %s/%s for DPU-host simulated VF (%s)", namespace, deploymentName, vfResourceName)
	return nil
}

// patchContainerVFRequest adds the simulated VF resource name and quantity to the container requests and limits
func patchContainerVFRequest(co *corev1.Container, rn corev1.ResourceName, qty resource.Quantity) {
	if co.Resources.Requests == nil {
		co.Resources.Requests = corev1.ResourceList{}
	}
	if co.Resources.Limits == nil {
		co.Resources.Limits = corev1.ResourceList{}
	}
	co.Resources.Requests[rn] = qty
	co.Resources.Limits[rn] = qty
}

// mergeDPUHostNodeAffinity ensures pod schedules only on nodes labeled with the DPU Host node label
func mergeDPUHostNodeAffinity(pod *corev1.PodSpec) {
	req := corev1.NodeSelectorRequirement{
		Key:      config.DPUHostNodeLabelKey,
		Operator: corev1.NodeSelectorOpExists,
	}
	if pod.Affinity == nil {
		pod.Affinity = &corev1.Affinity{}
	}
	if pod.Affinity.NodeAffinity == nil {
		pod.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	na := pod.Affinity.NodeAffinity
	if na.RequiredDuringSchedulingIgnoredDuringExecution == nil || len(na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) == 0 {
		na.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{req}},
			},
		}
		return
	}
	term := &na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0]
	for _, e := range term.MatchExpressions {
		if e.Key == config.DPUHostNodeLabelKey && e.Operator == corev1.NodeSelectorOpExists {
			return
		}
	}
	term.MatchExpressions = append(term.MatchExpressions, req)
}
