package k8s

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	discoverycache "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

// deploymentSuspendReplicasAnnotationKey stores the pre-scale-down replica count
// on a Deployment while it is suspended at zero replicas (see SuspendDeployment).
const deploymentSuspendReplicasAnnotationKey = "dpu-sim.io/suspend-replicas"

// NewClient creates a new Kubernetes client from kubeconfig content
func NewClient(kubeconfigContent string) (*K8sClient, error) {
	clientConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigContent))
	if err != nil {
		return nil, fmt.Errorf("failed to create REST config from kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}

	// Use a cached discovery client that can be invalidated when new CRDs are created
	cachedDiscovery := discoverycache.NewMemCacheClient(discoveryClient)

	// Use DeferredDiscoveryRESTMapper which will refresh its cache when it encounters
	// unknown resources (e.g., after CRDs are created)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedDiscovery)

	return &K8sClient{
		clientset:       clientset,
		dynamicClient:   dynamicClient,
		cachedDiscovery: cachedDiscovery,
		restMapper:      mapper,
		apiServerURL:    clientConfig.Host,
	}, nil
}

// GetAPIServerURL returns the API server URL from the kubeconfig used to
// create this client (e.g., "https://192.168.123.11:6443").
func (c *K8sClient) GetAPIServerURL() string {
	return c.apiServerURL
}

// NewClientFromFile creates a new Kubernetes client from a kubeconfig file path
func NewClientFromFile(kubeconfigPath string) (*K8sClient, error) {
	kubeconfigContent, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read kubeconfig file %s: %w", kubeconfigPath, err)
	}

	return NewClient(string(kubeconfigContent))
}

// Clientset returns the underlying Kubernetes clientset for advanced operations
func (c *K8sClient) Clientset() *kubernetes.Clientset {
	return c.clientset
}

// DynamicClient returns the underlying dynamic client for unstructured operations
func (c *K8sClient) DynamicClient() dynamic.Interface {
	return c.dynamicClient
}

// InvalidateDiscoveryCache invalidates the cached API discovery information.
// Call this after creating CRDs so the client can discover the new resource types.
func (c *K8sClient) InvalidateDiscoveryCache() {
	c.cachedDiscovery.Invalidate()
	c.restMapper.Reset()
}

// ApplyManifest applies a YAML manifest (potentially with multiple objects) to the cluster
func (c *K8sClient) ApplyManifest(manifest []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	return c.applyManifest(ctx, manifest)
}

// ApplyManifestFromURL downloads and applies a YAML manifest from a URL
func (c *K8sClient) ApplyManifestFromURL(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Download the manifest
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download manifest from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download manifest: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read manifest body: %w", err)
	}

	return c.applyManifest(ctx, body)
}

func (c *K8sClient) applyManifest(ctx context.Context, manifest []byte) error {
	decoder := yaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(manifest)))

	for {
		doc, err := decoder.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read YAML document: %w", err)
		}

		// Skip empty yamls
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(doc, obj); err != nil {
			return fmt.Errorf("failed to unmarshal YAML document: %w", err)
		}

		// Skip empty objects
		if obj.GetKind() == "" {
			continue
		}

		if err := c.applyResource(ctx, obj); err != nil {
			return fmt.Errorf("failed to apply %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}

	return nil
}

// applyResource applies a single Kubernetes resource to the cluster
func (c *K8sClient) applyResource(ctx context.Context, obj *unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()

	// Use RESTMapper to get the correct GVR from the API server's discovery
	mapping, err := c.restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("failed to get REST mapping for %s: %w", gvk, err)
	}
	gvr := mapping.Resource

	namespace := obj.GetNamespace()

	// Use the mapping's scope to determine if resource is namespaced
	resourceClient := c.dynamicClient.Resource(gvr)
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		// Namespaced resource
		if namespace == "" {
			namespace = "default"
		}
		existing, err := resourceClient.Namespace(namespace).Get(ctx, obj.GetName(), metav1.GetOptions{})
		if err == nil {
			obj.SetResourceVersion(existing.GetResourceVersion())
			_, err = resourceClient.Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to update: %w", err)
			}
			log.Debug("Updated %s/%s in namespace %s", gvk.Kind, obj.GetName(), namespace)
		} else {
			_, err = resourceClient.Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create: %w", err)
			}
			log.Debug("Created %s/%s in namespace %s", gvk.Kind, obj.GetName(), namespace)
		}
	} else {
		// Cluster-scoped resource
		existing, err := resourceClient.Get(ctx, obj.GetName(), metav1.GetOptions{})
		if err == nil {
			obj.SetResourceVersion(existing.GetResourceVersion())
			_, err = resourceClient.Update(ctx, obj, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to update: %w", err)
			}
			log.Debug("Updated %s/%s", gvk.Kind, obj.GetName())
		} else {
			_, err = resourceClient.Create(ctx, obj, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create: %w", err)
			}
			log.Debug("Created %s/%s", gvk.Kind, obj.GetName())
		}
	}

	return nil
}

// ListPods lists pods in a namespace with an optional label selector
func (c *K8sClient) ListPods(namespace, labelSelector string) ([]corev1.Pod, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	return pods.Items, nil
}

// GetPod gets a specific pod by name and namespace
func (c *K8sClient) GetPod(namespace, name string) (*corev1.Pod, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
}

// DeletePod deletes a specific pod by name and namespace
func (c *K8sClient) DeletePod(namespace, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// CreatePod creates a new pod
func (c *K8sClient) CreatePod(pod *corev1.Pod) (*corev1.Pod, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
}

// IsPodReady checks if a pod is in Ready condition
func IsPodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// WaitForPodRunning waits for a specific pod to be in Running state
func (c *K8sClient) WaitForPodRunning(namespace, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for pod %s/%s to be running", namespace, name)
		case <-ticker.C:
			pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				continue
			}
			if pod.Status.Phase == corev1.PodRunning && IsPodReady(pod) {
				return nil
			}
		}
	}
}

// WaitForPodsReady waits for pods to be ready. If labelSelector is empty,
// it waits for all pods in the namespace.
func (c *K8sClient) WaitForPodsReady(namespace, labelSelector string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	if labelSelector != "" {
		log.Info("Waiting for pods in namespace: %s label: %s to be ready...", namespace, labelSelector)
	} else {
		log.Info("Waiting for all pods in namespace: %s to be ready...", namespace)
	}

	for {
		select {
		case <-ctx.Done():
			if labelSelector != "" {
				return fmt.Errorf("timeout waiting for pods in namespace: %s label: %s", namespace, labelSelector)
			} else {
				return fmt.Errorf("timeout waiting for all pods in namespace: %s", namespace)
			}
		case <-ticker.C:
			pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: labelSelector,
			})
			if err != nil {
				if labelSelector != "" {
					log.Warn("Warning: failed to list pods in namespace: %s label: %s: %v", namespace, labelSelector, err)
				} else {
					log.Warn("Warning: failed to list all pods in namespace: %s: %v", namespace, err)
				}
				continue
			}

			if len(pods.Items) == 0 {
				if labelSelector != "" {
					log.Debug("No pods found in namespace: %s label: %s", namespace, labelSelector)
				} else {
					log.Debug("No pods found in namespace: %s", namespace)
				}
				continue
			}

			allReady := true
			readyCount := 0
			for _, pod := range pods.Items {
				if IsPodReady(&pod) {
					readyCount++
				} else {
					allReady = false
				}
			}

			if labelSelector != "" {
				log.Debug("✓ Pods in namespace: %s label: %s ready: %d/%d", namespace, labelSelector, readyCount, len(pods.Items))
			} else {
				log.Debug("✓ All Pods in namespace: %s ready: %d/%d", namespace, readyCount, len(pods.Items))
			}

			if allReady {
				if labelSelector != "" {
					log.Info("✓ Pods in namespace: %s label: %s are ready", namespace, labelSelector)
				} else {
					log.Info("✓ All Pods in namespace: %s are ready", namespace)
				}
				return nil
			}
		}
	}
}

// WaitForDeploymentAvailable waits for a deployment to report Available=True.
func (c *K8sClient) WaitForDeploymentAvailable(namespace, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	log.Info("Waiting for deployment %s/%s to be available...", namespace, name)

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for deployment %s/%s to be available", namespace, name)
		case <-ticker.C:
			deployment, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				log.Warn("Warning: failed to get deployment %s/%s: %v", namespace, name, err)
				continue
			}

			if deployment.Status.ObservedGeneration < deployment.Generation {
				continue
			}

			if isDeploymentAvailable(deployment) {
				log.Info("✓ Deployment %s/%s is available", namespace, name)
				return nil
			}
		}
	}
}

// WaitForDeploymentPodsReady waits until a Deployment is available and all
// pods matching its selector are Ready.
func (c *K8sClient) WaitForDeploymentPodsReady(namespace, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	log.Info("Waiting for deployment %s/%s pods to be ready...", namespace, name)

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for deployment %s/%s pods to be ready", namespace, name)
		case <-ticker.C:
			deployment, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				log.Warn("Warning: failed to get deployment %s/%s: %v", namespace, name, err)
				continue
			}
			if deployment.Status.ObservedGeneration < deployment.Generation || !isDeploymentAvailable(deployment) {
				continue
			}
			desiredReplicas := int32(1)
			if deployment.Spec.Replicas != nil {
				desiredReplicas = *deployment.Spec.Replicas
			}

			selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
			if err != nil {
				return fmt.Errorf("deployment %s/%s has invalid selector: %w", namespace, name, err)
			}
			if selector.Empty() {
				selector = labels.Nothing()
			}

			pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: selector.String(),
			})
			if err != nil {
				log.Warn("Warning: failed to list pods for deployment %s/%s: %v", namespace, name, err)
				continue
			}
			if len(pods.Items) == 0 {
				continue
			}

			allReady := true
			readyCount := int32(0)
			for _, pod := range pods.Items {
				if IsPodReady(&pod) {
					readyCount++
				} else {
					allReady = false
				}
			}
			log.Debug("Deployment %s/%s pods ready: %d/%d listed, desired %d", namespace, name, readyCount, len(pods.Items), desiredReplicas)
			if allReady && readyCount >= desiredReplicas {
				log.Info("✓ Deployment %s/%s pods are ready: %d/%d", namespace, name, readyCount, desiredReplicas)
				return nil
			}
		}
	}
}

func isDeploymentAvailable(deployment *appsv1.Deployment) bool {
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// GetNodes returns all nodes in the cluster
func (c *K8sClient) GetNodes() ([]corev1.Node, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nodes, err := c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	return nodes.Items, nil
}

// GetNode gets a specific node by name
func (c *K8sClient) GetNode(name string) (*corev1.Node, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
}

// LabelNode adds or updates labels on a node
func (c *K8sClient) LabelNode(name string, labels map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := c.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get node %s: %w", name, err)
		}

		if node.Labels == nil {
			node.Labels = make(map[string]string)
		}
		for k, v := range labels {
			node.Labels[k] = v
		}

		_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to update node %s labels: %w", name, err)
	}
	return nil
}

// AnnotateNode adds or updates annotations on a node
func (c *K8sClient) AnnotateNode(name string, annotations map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := c.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get node %s: %w", name, err)
		}

		if node.Annotations == nil {
			node.Annotations = make(map[string]string)
		}
		for k, v := range annotations {
			node.Annotations[k] = v
		}

		_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to update node %s annotations: %w", name, err)
	}
	return nil
}

// RemoveNodeTaint removes a taint from a node by key and effect (best effort, ignores if not found)
func (c *K8sClient) RemoveNodeTaint(name string, taintKey string, effect corev1.TaintEffect) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node, err := c.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get node %s: %w", name, err)
		}

		// Filter out the taint we want to remove
		newTaints := []corev1.Taint{}
		for _, taint := range node.Spec.Taints {
			if taint.Key == taintKey && taint.Effect == effect {
				continue // Skip this taint (remove it)
			}
			newTaints = append(newTaints, taint)
		}

		node.Spec.Taints = newTaints

		_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to update node %s taints: %w", name, err)
	}
	return nil
}

// GetNamespaces returns all namespaces in the cluster
func (c *K8sClient) GetNamespaces() ([]corev1.Namespace, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	namespaces, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}

	return namespaces.Items, nil
}

// GetNamespace gets a specific namespace by name
func (c *K8sClient) GetNamespace(name string) (*corev1.Namespace, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
}

// CreateNamespace creates a new namespace
func (c *K8sClient) CreateNamespace(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	_, err := c.clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	return err
}

// DeleteNamespace deletes a namespace
func (c *K8sClient) DeleteNamespace(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
}

// GetServices returns all services in a namespace
func (c *K8sClient) GetServices(namespace string) ([]corev1.Service, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	services, err := c.clientset.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list services: %w", err)
	}

	return services.Items, nil
}

// GetService gets a specific service by name and namespace
func (c *K8sClient) GetService(namespace, name string) (*corev1.Service, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
}

// CreateService creates a new service
func (c *K8sClient) CreateService(service *corev1.Service) (*corev1.Service, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Services(service.Namespace).Create(ctx, service, metav1.CreateOptions{})
}

// DeleteService deletes a service
func (c *K8sClient) DeleteService(namespace, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Services(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// GetConfigMap gets a ConfigMap by name and namespace
func (c *K8sClient) GetConfigMap(namespace, name string) (*corev1.ConfigMap, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
}

// CreateConfigMap creates a new ConfigMap
func (c *K8sClient) CreateConfigMap(configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().ConfigMaps(configMap.Namespace).Create(ctx, configMap, metav1.CreateOptions{})
}

// UpdateConfigMap updates an existing ConfigMap
func (c *K8sClient) UpdateConfigMap(configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().ConfigMaps(configMap.Namespace).Update(ctx, configMap, metav1.UpdateOptions{})
}

// DeleteConfigMap deletes a ConfigMap
func (c *K8sClient) DeleteConfigMap(namespace, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// GetSecret gets a Secret by name and namespace
func (c *K8sClient) GetSecret(namespace, name string) (*corev1.Secret, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
}

// CreateSecret creates a new Secret
func (c *K8sClient) CreateSecret(secret *corev1.Secret) (*corev1.Secret, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})
}

// UpdateSecret updates an existing Secret
func (c *K8sClient) UpdateSecret(secret *corev1.Secret) (*corev1.Secret, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
}

// DeleteSecret deletes a Secret
func (c *K8sClient) DeleteSecret(namespace, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return c.clientset.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// RolloutRestartDaemonSet triggers a rolling restart of a DaemonSet by updating
// a pod template annotation with the current timestamp
func (c *K8sClient) RolloutRestartDaemonSet(namespace, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	daemonSet, err := c.clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get daemonset %s/%s: %w", namespace, name, err)
	}

	// Update the pod template annotation to trigger a rollout
	if daemonSet.Spec.Template.Annotations == nil {
		daemonSet.Spec.Template.Annotations = make(map[string]string)
	}
	daemonSet.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)

	_, err = c.clientset.AppsV1().DaemonSets(namespace).Update(ctx, daemonSet, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update daemonset %s/%s: %w", namespace, name, err)
	}

	log.Info("✓ Triggered rollout restart for daemonset %s/%s", namespace, name)
	return nil
}

// RolloutRestartDeployment triggers a rolling restart of a Deployment by updating
// a pod template annotation with the current timestamp.
// If ignoreNotFound is true and the Deployment does not exist, it returns nil.
func (c *K8sClient) RolloutRestartDeployment(namespace, name string, ignoreNotFound bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	deployment, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if ignoreNotFound && apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get deployment %s/%s: %w", namespace, name, err)
	}

	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = make(map[string]string)
	}
	deployment.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)

	_, err = c.clientset.AppsV1().Deployments(namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update deployment %s/%s: %w", namespace, name, err)
	}

	log.Info("✓ Triggered rollout restart for deployment %s/%s", namespace, name)
	return nil
}

// SuspendDeployment scales a Deployment to zero after recording the prior
// .spec.replicas value in an annotation so ResumeDeployment can restore it.
func (c *K8sClient) SuspendDeployment(namespace, name string, ignoreNotFound bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if ignoreNotFound && apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}

	if dep.Spec.Replicas != nil && *dep.Spec.Replicas == 0 {
		return nil
	}

	prev := int32(1)
	if dep.Spec.Replicas != nil {
		prev = *dep.Spec.Replicas
	}
	if dep.ObjectMeta.Annotations == nil {
		dep.ObjectMeta.Annotations = map[string]string{}
	}
	if _, already := dep.ObjectMeta.Annotations[deploymentSuspendReplicasAnnotationKey]; !already {
		dep.ObjectMeta.Annotations[deploymentSuspendReplicasAnnotationKey] = strconv.FormatInt(int64(prev), 10)
	}

	zero := int32(0)
	dep.Spec.Replicas = &zero

	if _, err := c.clientset.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("suspend deployment %s/%s: %w", namespace, name, err)
	}
	log.Info("✓ Scaled deployment %s/%s to 0 (saved replicas in %s)", namespace, name, deploymentSuspendReplicasAnnotationKey)
	return nil
}

// ResumeDeployment restores .spec.replicas from the value saved by SuspendDeployment.
// If that annotation is absent, .spec.replicas is set to 1.
func (c *K8sClient) ResumeDeployment(namespace, name string, ignoreNotFound bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if ignoreNotFound && apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}

	saved := ""
	if dep.ObjectMeta.Annotations != nil {
		saved = dep.ObjectMeta.Annotations[deploymentSuspendReplicasAnnotationKey]
	}

	replicas := int32(1)
	if saved != "" {
		n, err := strconv.ParseInt(saved, 10, 32)
		if err != nil || n < 1 {
			log.Warn("invalid saved replica count %q on %s/%s, defaulting to 1", saved, namespace, name)
			n = 1
		}
		replicas = int32(n)
		if dep.ObjectMeta.Annotations != nil {
			delete(dep.ObjectMeta.Annotations, deploymentSuspendReplicasAnnotationKey)
		}
	} else {
		log.Warn("deployment %s/%s has no %s annotation; scaling to 1 replica", namespace, name, deploymentSuspendReplicasAnnotationKey)
	}

	dep.Spec.Replicas = &replicas

	if _, err := c.clientset.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("resume deployment %s/%s: %w", namespace, name, err)
	}
	log.Info("✓ Restored deployment %s/%s to %d replicas", namespace, name, replicas)
	return nil
}

// DeletePodsByLabel deletes all pods in a namespace matching a label selector.
func (c *K8sClient) DeletePodsByLabel(namespace, labelSelector string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list pods in %s with label %s: %w", namespace, labelSelector, err)
	}

	for _, pod := range pods.Items {
		if err := c.clientset.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
			log.Warn("Warning: failed to delete pod %s/%s: %v", namespace, pod.Name, err)
		} else {
			log.Info("✓ Deleted pod %s/%s", namespace, pod.Name)
		}
	}

	return nil
}

// DeleteDaemonSet deletes a DaemonSet by name from the specified namespace.
func (c *K8sClient) DeleteDaemonSet(namespace, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Check if the DaemonSet exists
	_, err := c.clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("DaemonSet %s/%s does not exist, skipping deletion", namespace, name)
			return nil
		}
		return fmt.Errorf("failed to get daemonset %s/%s: %w", namespace, name, err)
	}

	if err := c.clientset.AppsV1().DaemonSets(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("failed to delete daemonset %s/%s: %w", namespace, name, err)
	}

	log.Info("✓ Deleted DaemonSet %s/%s", namespace, name)
	return nil
}
