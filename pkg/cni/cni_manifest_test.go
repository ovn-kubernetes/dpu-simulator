package cni

import (
	"testing"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

func TestShouldUseWritableCNIBinDir(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "kind uses default upstream cni paths",
			cfg: &config.Config{
				Kind: &config.KindConfig{
					Nodes: []config.KindNodeConfig{
						{Name: "cp", K8sCluster: "c", K8sRole: "control-plane"},
					},
				},
			},
			want: false,
		},
		{
			name: "vm mode uses default upstream cni paths",
			cfg: &config.Config{
				VMs: []config.VMConfig{{Name: "vm1"}},
			},
			want: false,
		},
		{
			name: "bare metal without bootc uses default upstream cni paths",
			cfg: &config.Config{
				BareMetal: []config.BareMetalConfig{{Name: "n1"}},
			},
			want: false,
		},
		{
			name: "operating_system image_ref uses writable cni bin",
			cfg: &config.Config{
				VMs:             []config.VMConfig{{Name: "vm1"}},
				OperatingSystem: config.OSConfig{ImageRef: "quay.io/example/os:latest"},
			},
			want: true,
		},
		{
			name: "bare metal bootc uses writable cni bin",
			cfg: &config.Config{
				BareMetal: []config.BareMetalConfig{{
					Name:  "n1",
					Bootc: &config.BareMetalBootcConfig{Enabled: true},
				}},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, err := NewCNIManager(tt.cfg, platform.NewLocalExecutor())
			if err != nil {
				t.Fatal(err)
			}
			if got := m.shouldUseWritableCNIBinDir(); got != tt.want {
				t.Fatalf("shouldUseWritableCNIBinDir() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestImagesFromManifest(t *testing.T) {
	manifest := []byte(`
apiVersion: apps/v1
kind: DaemonSet
spec:
  template:
    spec:
      initContainers:
      - name: install
        image: ghcr.io/flannel-io/flannel-cni-plugin:v1.9.1-flannel3
      - image: "ghcr.io/flannel-io/flannel:v0.28.9"
      containers:
      - name: main
        image: 'ghcr.io/flannel-io/flannel:v0.28.9'
        env:
        - name: NOT_AN_IMAGE
          value: "image: fake/nope:1"
`)
	got := imagesFromManifest(manifest)
	want := []string{
		"ghcr.io/flannel-io/flannel-cni-plugin:v1.9.1-flannel3",
		"ghcr.io/flannel-io/flannel:v0.28.9",
	}
	if len(got) != len(want) {
		t.Fatalf("imagesFromManifest = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("imagesFromManifest[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
