package tft

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"gopkg.in/yaml.v3"
)

// prepareTFTConfig prepares both embedded and external profiles for the selected
// deployment. Preserve harness extensions and explicit node names verbatim.
func prepareTFTConfig(cfg *config.Config, configPath string, opts RunOptions) ([]byte, error) {
	var data []byte
	var err error
	if strings.TrimSpace(opts.TFTConfig) != "" {
		data, err = os.ReadFile(opts.TFTConfig)
	} else if HasEmbeddedTFT(cfg) {
		data, err = yaml.Marshal(map[string]any{"tft": cfg.TFT.Node()})
	} else {
		return nil, fmt.Errorf("no tft: in dpu-sim config; pass --tft-config or add tft: to the YAML")
	}
	if err != nil {
		return nil, fmt.Errorf("read tft config: %w", err)
	}
	var profile map[string]any
	if err := yaml.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("parse tft config: %w", err)
	}
	if profile == nil || profile["tft"] == nil {
		return nil, fmt.Errorf("tft config has no tft: section")
	}
	// Honor an external profile's kubeconfig, relative to that profile, unless
	// the caller explicitly selects a deployment cluster. Otherwise inject the
	// same deployment kubeconfig used for embedded profiles.
	profileKubeconfig := ""
	if kc, ok := profile["kubeconfig"].(string); ok {
		profileKubeconfig = strings.TrimSpace(kc)
	}
	if profileKubeconfig != "" && opts.Cluster == "" {
		profile["kubeconfig"], err = resolvePathRelativeToConfig(opts.TFTConfig, profileKubeconfig)
	} else {
		profile["kubeconfig"], err = ResolveKubeconfigPath(cfg, configPath, opts.Cluster)
	}
	if err != nil {
		return nil, err
	}
	var (
		cluster string
		nodes   []string
	)
	tests, _ := profile["tft"].([]any)
	for _, test := range tests {
		t, _ := test.(map[string]any)
		connections, _ := t["connections"].([]any)
		for _, connection := range connections {
			c, _ := connection.(map[string]any)
			for _, side := range []string{"server", "client"} {
				endpoints, _ := c[side].([]any)
				for _, endpoint := range endpoints {
					e, _ := endpoint.(map[string]any)
					name, _ := e["name"].(string)
					if !strings.HasPrefix(name, "@host-") {
						continue
					}
					// Literal external profiles need no simulator topology.
					if nodes == nil {
						cluster, err = tftAliasCluster(cfg, opts, profileKubeconfig != "")
						if err != nil {
							return nil, err
						}
						nodes = cfg.GetDPUHostNodeNames(cluster)
					}
					i, err := strconv.Atoi(strings.TrimPrefix(name, "@host-"))
					if err != nil || i < 1 || i > len(nodes) {
						return nil, fmt.Errorf("TFT node alias %q cannot resolve: cluster %s has %d paired host nodes", name, cluster, len(nodes))
					}
					e["name"] = nodes[i-1]
				}
			}
		}
	}
	return yaml.Marshal(profile)
}

// tftAliasCluster returns the topology cluster that matches the effective
// kubeconfig selection. A standalone profile kubeconfig has no simulator
// cluster metadata, so @host-N aliases are rejected unless --cluster supplies
// the missing mapping.
func tftAliasCluster(cfg *config.Config, opts RunOptions, profileKubeconfig bool) (string, error) {
	cluster := strings.TrimSpace(opts.Cluster)
	if profileKubeconfig && cluster == "" {
		return "", fmt.Errorf("TFT profile kubeconfig is ambiguous for @host-N aliases; pass --cluster")
	}

	// ResolveKubeconfigPath intentionally gives TFT_KUBECONFIG and the dpu-sim
	// config's kubeconfig field precedence over --cluster. Use the same default
	// topology for aliases instead of substituting nodes from an ignored flag.
	if strings.TrimSpace(os.Getenv("TFT_KUBECONFIG")) != "" || strings.TrimSpace(cfg.TrafficFlowTestsKubeconfig) != "" {
		cluster = ""
	}
	if cluster == "" {
		var err error
		cluster, err = tftDefaultCluster(cfg)
		if err != nil {
			return "", err
		}
	}
	return cluster, nil
}
