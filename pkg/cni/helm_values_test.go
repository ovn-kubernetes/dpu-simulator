package cni

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelmValuesToMap(t *testing.T) {
	values, err := helmValuesToMap([]helmValue{
		{key: "global.image.repository", value: "repo"},
		{key: "global.image.tag", value: "tag"},
		{key: "tags.ovs-node", value: false},
		{key: "ovnkube-node-dpu-host.mgmtPortVFsCount", value: 5},
	})
	require.NoError(t, err)

	assert.Equal(t, map[string]any{
		"global": map[string]any{
			"image": map[string]any{
				"repository": "repo",
				"tag":        "tag",
			},
		},
		"tags": map[string]any{
			"ovs-node": false,
		},
		"ovnkube-node-dpu-host": map[string]any{
			"mgmtPortVFsCount": 5,
		},
	}, values)
}

func TestAppendHelmSetArgsPreservesOrder(t *testing.T) {
	args := appendHelmSetArgs([]string{"install"}, []helmValue{
		{key: "global.image.repository", value: "repo"},
		{key: "tags.ovs-node", value: false},
		{key: "ovnkube-node-dpu-host.mgmtPortVFsCount", value: 5},
	})

	assert.Equal(t, []string{
		"install",
		"--set", "global.image.repository=repo",
		"--set", "tags.ovs-node=false",
		"--set", "ovnkube-node-dpu-host.mgmtPortVFsCount=5",
	}, args)
}

func TestPodCIDRWithPerNodePrefix(t *testing.T) {
	tests := []struct {
		name     string
		podCIDR  string
		expected string
		wantErr  bool
	}{
		{
			name:     "cluster CIDR gets default host subnet prefix",
			podCIDR:  "10.244.0.0/16",
			expected: "10.244.0.0/16/24",
		},
		{
			name:    "slash 24 cluster CIDR is too small for default host subnet prefix",
			podCIDR: "10.244.0.0/24",
			wantErr: true,
		},
		{
			name:     "existing host subnet prefix is preserved",
			podCIDR:  "10.244.0.0/16/26",
			expected: "10.244.0.0/16/26",
		},
		{
			name:    "existing host subnet prefix must be larger than cluster prefix",
			podCIDR: "10.244.0.0/24/24",
			wantErr: true,
		},
		{
			name:    "invalid pod CIDR errors",
			podCIDR: "not-a-cidr",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := podCIDRWithPerNodePrefix(tt.podCIDR)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestVMFRREnvDescribesVMTopology(t *testing.T) {
	cfg, err := config.LoadConfig("../../config-ovnk-offload.yaml")
	require.NoError(t, err)
	cfg.Kubernetes.KubeconfigDir = t.TempDir()
	m := &CNIManager{config: cfg}
	require.NoError(t, m.writeFRRK8sRemoteEnv("dpu-sim-dpu", "/tmp/remote", "/tmp/host"))
	b, err := os.ReadFile(filepath.Join(cfg.Kubernetes.KubeconfigDir, "helm-values", "dpu-sim-dpu-frr-k8s.env"))
	require.NoError(t, err)
	require.Contains(t, string(b), `DPU_SIM_GATEWAY_NETWORK="dpu-sim-gateway"`)
	require.Contains(t, string(b), `DPU_SIM_GATEWAY_SUBNET="172.30.0.0/24"`)
	require.Contains(t, string(b), `FRR_K8S_REMOTE_NODE_MAP="host-1-1=dpu-1-1,host-2-1=dpu-2-1"`)
}

func TestDPUHostUDNMasqueradeAddressSpace(t *testing.T) {
	cfg, err := config.LoadConfig("../../config-ovnk-offload.yaml")
	require.NoError(t, err)
	m := &CNIManager{config: cfg}
	overrides, err := m.ovnkHelmOverrides(ovnkModeDPUHost, "dpu-sim-host", "test:latest", false, true)
	require.NoError(t, err)
	values := map[string]any{}
	for _, override := range overrides {
		values[override.key] = override.value
	}
	// Upstream Kind reserves space for per-UDN masquerade addresses. The
	// chart's default IPv4 /29 has no room even for network ID 2.
	require.Equal(t, "169.254.0.0/17", values["global.v4MasqueradeSubnet"])
	require.Equal(t, "fd69::/112", values["global.v6MasqueradeSubnet"])
}
