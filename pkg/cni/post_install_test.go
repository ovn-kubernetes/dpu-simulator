package cni

import (
	"testing"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
)

func TestDeferSystemDeploymentsUntilExternalOVNK(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.Config
		clusterName string
		want        bool
	}{
		{
			name: "install mode resumes system deployments",
			cfg: &config.Config{
				OVNKubernetesMode: config.OVNKubernetesModeInstall,
				Kubernetes: config.KubernetesConfig{
					Clusters: []config.ClusterConfig{{Name: "host", CNI: config.CNIOVNKubernetes}},
				},
			},
			clusterName: "host",
			want:        false,
		},
		{
			name: "values-only OVN-Kubernetes host cluster defers system deployments",
			cfg: &config.Config{
				OVNKubernetesMode: config.OVNKubernetesModeValuesOnly,
				Kubernetes: config.KubernetesConfig{
					Clusters: []config.ClusterConfig{{Name: "host", CNI: config.CNIOVNKubernetes}},
				},
			},
			clusterName: "host",
			want:        true,
		},
		{
			name: "values-only DPU cluster defers when DPU OVN-Kubernetes is external",
			cfg: &config.Config{
				OVNKubernetesMode: config.OVNKubernetesModeValuesOnly,
				Kubernetes: config.KubernetesConfig{
					OffloadDPU: true,
					Clusters: []config.ClusterConfig{
						{Name: "host", CNI: config.CNIOVNKubernetes},
						{Name: "dpu", CNI: config.CNIFlannel},
					},
				},
				Kind: &config.KindConfig{
					Nodes: []config.KindNodeConfig{
						{Name: "host-worker", Type: config.HostType, K8sCluster: "host"},
						{Name: "dpu-worker", Type: config.DpuType, K8sCluster: "dpu", Host: "host-worker"},
					},
				},
			},
			clusterName: "dpu",
			want:        true,
		},
		{
			name: "values-only unrelated CNI does not defer system deployments",
			cfg: &config.Config{
				OVNKubernetesMode: config.OVNKubernetesModeValuesOnly,
				Kubernetes: config.KubernetesConfig{
					Clusters: []config.ClusterConfig{{Name: "flannel", CNI: config.CNIFlannel}},
				},
			},
			clusterName: "flannel",
			want:        false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := deferSystemDeploymentsUntilExternalOVNK(tc.cfg, tc.clusterName)
			if got != tc.want {
				t.Fatalf("got %t, want %t", got, tc.want)
			}
		})
	}
}
