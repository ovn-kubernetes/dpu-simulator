package cni

import (
	"testing"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/deviceplugin"
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

func TestDPUHostHelmOverridesIncludeMgmtPortResource(t *testing.T) {
	cfg := &config.Config{
		Networks: []config.NetworkConfig{
			{
				Name:             "host-to-dpu",
				Type:             config.HostToDpuNetworkType,
				NumPairs:         8,
				MgmtPortVFsCount: 3,
			},
		},
	}
	m := &CNIManager{config: cfg}

	overrides, err := m.ovnkHelmOverrides(ovnkModeDPUHost, "dpu-sim-host", DefaultOVNImage, true, true)
	require.NoError(t, err)
	values, err := helmValuesToMap(overrides)
	require.NoError(t, err)

	dpuHostValues := values["ovnkube-node-dpu-host"].(map[string]any)
	assert.Equal(t, deviceplugin.VFResourceName, dpuHostValues["mgmtPortVFResourceName"])
	assert.Equal(t, 3, dpuHostValues["mgmtPortVFsCount"])
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
