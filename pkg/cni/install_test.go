package cni

import (
	"testing"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestResolveAddonInstallOrderPassthroughWithoutWhereaboutsAddon(t *testing.T) {
	addons := []config.AddonType{config.AddonMultus, config.AddonCertManager}
	ordered := resolveAddonInstallOrder(addons)
	require.Equal(t, addons, ordered)
}

func TestResolveAddonInstallOrderDoesNotDuplicateWhereabouts(t *testing.T) {
	addons := []config.AddonType{config.AddonWhereabouts, config.AddonMultus, config.AddonCertManager}
	ordered := resolveAddonInstallOrder(addons)
	require.Equal(t, addons, ordered)
}

func TestPartitionPreCNIAddons(t *testing.T) {
	ordered := []config.AddonType{config.AddonWhereabouts, config.AddonMultus, config.AddonCertManager}
	pre, post := partitionPreCNIAddons(ordered)
	require.Equal(t, []config.AddonType{config.AddonWhereabouts, config.AddonMultus}, pre)
	require.Equal(t, []config.AddonType{config.AddonCertManager}, post)
}
