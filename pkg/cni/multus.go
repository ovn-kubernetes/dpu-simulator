package cni

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/deviceplugin"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

// MultusManifestURL is the URL for the Multus CNI manifest
const MultusManifestURL = "https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/master/deployments/multus-daemonset-thick.yml"

// installMultus installs Multus CNI using the Kubernetes API.
// When OVN-Kubernetes is the cluster's CNI, the thick daemon config is patched
// so Multus delegates the default pod network to the "ovn-primary" Network
// Attachment Definition instead of auto-discovering from the CNI config
// directory.
// See: https://ovn-kubernetes.io/blog/dpu-acceleration/#install-multus
func (m *CNIManager) installMultus(clusterName, apiServerHost string) error {
	log.Debug("Installing Multus CNI...")

	manifest, err := downloadManifest(MultusManifestURL)
	if err != nil {
		return fmt.Errorf("failed to download Multus manifest: %w", err)
	}

	if m.shouldUseWritableCNIBinDir() {
		manifest = rewriteCNIBinPath(manifest, writableCNIBinDir)
		manifest = patchMultusDaemonConfig(manifest, map[string]any{
			"binDir": writableCNIBinDir,
		})
		log.Info("Patching Multus CNI host paths and binDir to %s (delegate plugins + shim)", writableCNIBinDir)
	}

	if m.clusterUsesOVNKubernetes(clusterName) {
		manifest = patchMultusDaemonConfig(manifest, map[string]any{
			"multusNamespace":        "default",
			"clusterNetwork":         "ovn-primary",
			"readinessindicatorfile": "/host/etc/cni/net.d/10-ovn-kubernetes.conf",
		})
		log.Info("Patching Multus daemon config for OVN-Kubernetes (multusNamespace=default, clusterNetwork=ovn-primary)")
	}

	if err := m.k8sClient.ApplyManifest(manifest); err != nil {
		return fmt.Errorf("failed to install Multus: %w", err)
	}

	if strings.TrimSpace(apiServerHost) != "" {
		if err := m.patchMultusAPIServer(apiServerHost); err != nil {
			return err
		}
	}

	// The Multus manifest includes the NetworkAttachmentDefinition CRD.
	// Invalidate the discovery cache so we can create NAD resources below.
	m.k8sClient.InvalidateDiscoveryCache()

	if m.clusterUsesOVNKubernetes(clusterName) {
		if err := m.createOVNPrimaryNAD(clusterName); err != nil {
			return fmt.Errorf("failed to create ovn-primary NAD: %w", err)
		}
	}

	log.Info("✓ Multus is installed")

	// Wait for Multus daemonset pods to be ready.
	if err := m.k8sClient.WaitForPodsReady("kube-system", "name=multus", 3*time.Minute); err != nil {
		return fmt.Errorf("multus pods are not ready: %w", err)
	}

	return nil
}

func (m *CNIManager) patchMultusAPIServer(apiServerHost string) error {
	apiServerHost = strings.TrimSpace(apiServerHost)
	if apiServerHost == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ds, err := m.k8sClient.Clientset().AppsV1().DaemonSets("kube-system").Get(ctx, "kube-multus-ds", metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get Multus daemonset: %w", err)
		}

		for i := range ds.Spec.Template.Spec.Containers {
			if ds.Spec.Template.Spec.Containers[i].Name != "kube-multus" {
				continue
			}

			setContainerEnv(&ds.Spec.Template.Spec.Containers[i], "KUBERNETES_SERVICE_HOST", apiServerHost)
			setContainerEnv(&ds.Spec.Template.Spec.Containers[i], "KUBERNETES_SERVICE_PORT", "6443")
			_, err = m.k8sClient.Clientset().AppsV1().DaemonSets("kube-system").Update(ctx, ds, metav1.UpdateOptions{})
			return err
		}

		return fmt.Errorf("failed to find kube-multus container in Multus daemonset")
	}); err != nil {
		return fmt.Errorf("failed to patch Multus API endpoint: %w", err)
	}

	log.Info("✓ Patched Multus to use API endpoint https://%s:6443", apiServerHost)
	return nil
}

func setContainerEnv(container *corev1.Container, name, value string) {
	for i := range container.Env {
		if container.Env[i].Name == name {
			container.Env[i].Value = value
			container.Env[i].ValueFrom = nil
			return
		}
	}

	container.Env = append(container.Env, corev1.EnvVar{Name: name, Value: value})
}

func (m *CNIManager) clusterUsesOVNKubernetes(clusterName string) bool {
	return m.config.GetCNIType(clusterName) == config.CNIOVNKubernetes
}

// createOVNPrimaryNAD creates the "ovn-primary" NetworkAttachmentDefinition in
// the default namespace. This NAD tells Multus to delegate the default pod
// network to OVN-Kubernetes.
// When DPU offloading is enabled the NAD is annotated with the device plugin
// resource so pods get a VF allocated for their primary interface.
func (m *CNIManager) createOVNPrimaryNAD(clusterName string) error {
	annotations := ""
	if m.config.IsOffloadDPU() && !m.config.IsDPUCluster(clusterName) {
		annotations = fmt.Sprintf(`  annotations:
    k8s.v1.cni.cncf.io/resourceName: %s`, deviceplugin.VFResourceName)
	}

	nad := fmt.Sprintf(`apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: ovn-primary
  namespace: default
%s
spec:
  config: |
    {
      "cniVersion": "1.1.0",
      "name": "ovn-primary",
      "netAttachDefName": "default/ovn-primary",
      "type": "ovn-k8s-cni-overlay",
      "logFile": "/var/log/ovn-kubernetes/ovn-k8s-cni-overlay.log",
      "logLevel": "5",
      "logfile-maxsize": 100,
      "logfile-maxbackups": 5,
      "logfile-maxage": 5
    }
`, annotations)

	if err := m.k8sClient.ApplyManifest([]byte(nad)); err != nil {
		return fmt.Errorf("failed to apply ovn-primary NAD: %w", err)
	}
	log.Info("✓ Created ovn-primary NetworkAttachmentDefinition")
	return nil
}

// patchMultusDaemonConfig locates the daemon-config.json JSON block inside the
// Multus manifest ConfigMap, parses it, merges the supplied fields, and
// re-serialises it in place. This is safer than anchoring string replacements
// on a specific key that could change upstream.
func patchMultusDaemonConfig(manifest []byte, extraFields map[string]any) []byte {
	content := string(manifest)

	marker := "daemon-config.json:"
	markerIdx := strings.Index(content, marker)
	if markerIdx == -1 {
		log.Warn("daemon-config.json not found in Multus manifest, skipping config patch")
		return manifest
	}

	// Find the opening brace of the JSON object.
	relBrace := strings.Index(content[markerIdx:], "{")
	if relBrace == -1 {
		log.Warn("JSON block not found in Multus daemon-config, skipping config patch")
		return manifest
	}
	jsonStart := markerIdx + relBrace

	// Derive the YAML indentation prefix from whitespace before '{'.
	lineStart := strings.LastIndex(content[:jsonStart], "\n") + 1
	prefix := content[lineStart:jsonStart]

	// Find the matching closing brace via simple depth counting.
	depth, jsonEnd := 0, -1
	for i := jsonStart; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				jsonEnd = i + 1
			}
		}
		if jsonEnd != -1 {
			break
		}
	}
	if jsonEnd == -1 {
		log.Warn("Unmatched brace in Multus daemon-config JSON, skipping config patch")
		return manifest
	}

	var cfg map[string]any
	if err := json.Unmarshal([]byte(content[jsonStart:jsonEnd]), &cfg); err != nil {
		log.Warn("Failed to parse Multus daemon-config JSON: %v", err)
		return manifest
	}

	for k, v := range extraFields {
		cfg[k] = v
	}

	newJSON, err := json.MarshalIndent(cfg, prefix, "    ")
	if err != nil {
		log.Warn("Failed to marshal Multus daemon-config JSON: %v", err)
		return manifest
	}

	return []byte(content[:jsonStart] + string(newJSON) + content[jsonEnd:])
}
