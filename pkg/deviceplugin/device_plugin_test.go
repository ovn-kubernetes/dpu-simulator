package deviceplugin

import (
	"fmt"
	"testing"

	"github.com/ovn-kubernetes/dpu-simulator/lib/dpusim"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/stretchr/testify/assert"
)

// TestBuildResourcePools verifies mgmt and pod VF pools partition host data
// interfaces correctly for several mgmt_port_vfs_count values, including
// MatcherDescription output and non-overlapping MatchesIface selectors.
func TestBuildResourcePools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mgmtCount      int
		mgmtMatches    []string
		mgmtNonMatches []string
		podMatches     []string
		podNonMatches  []string
	}{
		{
			name:           "two mgmt VFs",
			mgmtCount:      2,
			mgmtMatches:    []string{dpusim.HostDataIf(1), dpusim.HostDataIf(2)},
			mgmtNonMatches: []string{dpusim.HostDataIf(0), dpusim.HostDataIf(3), dpusim.DPUDataIf(1)},
			podMatches:     []string{dpusim.HostDataIf(3), dpusim.HostDataIf(10)},
			podNonMatches:  []string{dpusim.HostDataIf(0), dpusim.HostDataIf(1), dpusim.HostDataIf(2)},
		},
		{
			name:           "three mgmt VFs",
			mgmtCount:      3,
			mgmtMatches:    []string{dpusim.HostDataIf(1), dpusim.HostDataIf(2), dpusim.HostDataIf(3)},
			mgmtNonMatches: []string{dpusim.HostDataIf(0), dpusim.HostDataIf(4)},
			podMatches:     []string{dpusim.HostDataIf(4), dpusim.HostDataIf(15)},
			podNonMatches:  []string{dpusim.HostDataIf(0), dpusim.HostDataIf(1), dpusim.HostDataIf(2), dpusim.HostDataIf(3)},
		},
		{
			name:           "single mgmt VF",
			mgmtCount:      1,
			mgmtMatches:    []string{dpusim.HostDataIf(1)},
			mgmtNonMatches: []string{dpusim.HostDataIf(0), dpusim.HostDataIf(2)},
			podMatches:     []string{dpusim.HostDataIf(2), dpusim.HostDataIf(9)},
			podNonMatches:  []string{dpusim.HostDataIf(0), dpusim.HostDataIf(1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pools := BuildResourcePools(tt.mgmtCount)
			assert.Len(t, pools, 2)

			mgmtPool := pools[0]
			podPool := pools[1]
			assert.Equal(t, MgmtVFResourceName, mgmtPool.ResourceName)
			assert.Equal(t, VFResourceName, podPool.ResourceName)

			if tt.mgmtCount == 1 {
				assert.Equal(t, dpusim.HostDataIf(1), mgmtPool.MatcherDescription())
			} else {
				assert.Equal(t, fmt.Sprintf("%s..%s", dpusim.HostDataIf(1), dpusim.HostDataIf(tt.mgmtCount)), mgmtPool.MatcherDescription())
			}
			assert.Equal(t, fmt.Sprintf("%s..", dpusim.HostDataIf(tt.mgmtCount+1)), podPool.MatcherDescription())

			for _, iface := range tt.mgmtMatches {
				assert.True(t, mgmtPool.MatchesIface(iface), "mgmt should match %s", iface)
				assert.False(t, podPool.MatchesIface(iface), "pod should not match %s", iface)
			}
			for _, iface := range tt.mgmtNonMatches {
				assert.False(t, mgmtPool.MatchesIface(iface), "mgmt should not match %s", iface)
			}
			for _, iface := range tt.podMatches {
				assert.True(t, podPool.MatchesIface(iface), "pod should match %s", iface)
				assert.False(t, mgmtPool.MatchesIface(iface), "mgmt should not match %s", iface)
			}
			for _, iface := range tt.podNonMatches {
				assert.False(t, podPool.MatchesIface(iface), "pod should not match %s", iface)
			}
		})
	}
}

// TestBuildResourcePoolsInvalidCountUsesDefault checks that a non-positive value
// mgmt_port_vfs_count falls back to DefaultMgmtPortVFsCount.
func TestBuildResourcePoolsInvalidCountUsesDefault(t *testing.T) {
	t.Parallel()

	pools := BuildResourcePools(0)
	for i := 1; i <= config.DefaultMgmtPortVFsCount; i++ {
		assert.True(t, pools[0].MatchesIface(dpusim.HostDataIf(i)))
	}
	firstPodVF := dpusim.HostDataIf(config.DefaultMgmtPortVFsCount + 1)
	assert.False(t, pools[0].MatchesIface(firstPodVF))
	assert.True(t, pools[1].MatchesIface(firstPodVF))
}

// TestMgmtPortVFsCountFromEnv checks parsing of MGMT_PORT_VFS_COUNT, including
// valid values and fallback to config.DefaultMgmtPortVFsCount when unset or invalid.
func TestMgmtPortVFsCountFromEnv(t *testing.T) {
	t.Setenv(MgmtPortVFsCountEnvVar, "5")
	assert.Equal(t, 5, MgmtPortVFsCountFromEnv())

	t.Setenv(MgmtPortVFsCountEnvVar, "")
	assert.Equal(t, config.DefaultMgmtPortVFsCount, MgmtPortVFsCountFromEnv())

	t.Setenv(MgmtPortVFsCountEnvVar, "invalid")
	assert.Equal(t, config.DefaultMgmtPortVFsCount, MgmtPortVFsCountFromEnv())
}
