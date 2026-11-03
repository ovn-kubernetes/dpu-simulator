package tft

import (
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
	"os"
	"path/filepath"
	"testing"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestHasEmbeddedTFT(t *testing.T) {
	t.Parallel()
	var empty config.Config
	require.False(t, HasEmbeddedTFT(&empty))

	n := yaml.Node{Kind: yaml.SequenceNode}
	cfg := config.Config{TFT: &config.TrafficFlowTestsSubtree{}}
	_ = cfg.TFT.UnmarshalYAML(&n)
	require.True(t, HasEmbeddedTFT(&cfg))
}

func TestTFTDefaultCluster_PrefersHostOverDPU(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Kind: &config.KindConfig{Nodes: []config.KindNodeConfig{
			{Name: "cp-dpu", K8sCluster: "dpu-c", K8sRole: "control-plane"},
			{Name: "dw", Type: config.DpuType, K8sCluster: "dpu-c", K8sRole: "worker", Host: "hw"},
			{Name: "cp-host", K8sCluster: "host-c", K8sRole: "control-plane"},
			{Name: "hw", Type: config.HostType, K8sCluster: "host-c", K8sRole: "worker"},
		}},
		Kubernetes: config.KubernetesConfig{
			Clusters: []config.ClusterConfig{
				{Name: "dpu-c", CNI: config.CNIFlannel},
				{Name: "host-c", CNI: config.CNIOVNKubernetes},
			},
		},
	}
	name, err := tftDefaultCluster(cfg)
	require.NoError(t, err)
	require.Equal(t, "host-c", name)
}

func TestResolveKubeconfigPath_TFTKubeconfigEnv(t *testing.T) {
	tmp := t.TempDir()
	envKC := filepath.Join(tmp, "from-env.kubeconfig")
	require.NoError(t, os.WriteFile(envKC, []byte("x"), 0o600))
	t.Setenv("TFT_KUBECONFIG", envKC)

	cfgPath := filepath.Join(tmp, "ci", "cfg.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
	cfg := &config.Config{
		TrafficFlowTestsKubeconfig: "kubeconfig/dpu-sim-host.kubeconfig",
		Kubernetes: config.KubernetesConfig{
			KubeconfigDir: "kubeconfig",
			Clusters:      []config.ClusterConfig{{Name: "dpu-sim-host"}},
		},
	}

	got, err := ResolveKubeconfigPath(cfg, cfgPath, "")
	require.NoError(t, err)
	require.Equal(t, envKC, got)
}

func TestResolveKubeconfigPath_DefaultCluster(t *testing.T) {
	t.Setenv("TFT_KUBECONFIG", "")
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	cfg := &config.Config{
		Kubernetes: config.KubernetesConfig{
			KubeconfigDir: "kubeconfig",
			Clusters: []config.ClusterConfig{
				{Name: "dpu-sim-host"},
			},
		},
	}
	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmp))
	defer func() { _ = os.Chdir(oldWd) }()
	require.NoError(t, os.MkdirAll("kubeconfig", 0o755))
	want := filepath.Join(tmp, "kubeconfig", "dpu-sim-host.kubeconfig")
	require.NoError(t, os.WriteFile(want, []byte("x"), 0o600))

	got, err := ResolveKubeconfigPath(cfg, cfgPath, "")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestResolveKubeconfigPath_MissingClusterKubeconfig(t *testing.T) {
	t.Setenv("TFT_KUBECONFIG", "")
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	cfg := &config.Config{
		Kubernetes: config.KubernetesConfig{
			KubeconfigDir: "kubeconfig",
			Clusters: []config.ClusterConfig{
				{Name: "missing-cluster"},
			},
		},
	}
	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmp))
	defer func() { _ = os.Chdir(oldWd) }()
	require.NoError(t, os.MkdirAll("kubeconfig", 0o755))

	_, err := ResolveKubeconfigPath(cfg, cfgPath, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing-cluster")
	require.Contains(t, err.Error(), "dpu-sim deploy")
}

func TestResolveKubeconfigPath_MissingYamlKubeconfig(t *testing.T) {
	t.Setenv("TFT_KUBECONFIG", "")
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("x"), 0o644))
	cfg := &config.Config{
		TrafficFlowTestsKubeconfig: "no-such.kubeconfig",
		Kubernetes: config.KubernetesConfig{
			Clusters: []config.ClusterConfig{{Name: "c"}},
		},
	}

	_, err := ResolveKubeconfigPath(cfg, cfgPath, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "kubeconfig:")
}

func TestGetDeploymentModeAllowsVM(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		VMs: []config.VMConfig{{
			Name: "worker-1", Type: config.HostType, K8sCluster: "cluster-1", K8sRole: "worker",
			K8sNodeMAC: "52:54:00:00:01:11", K8sNodeIP: "192.168.1.2", Memory: 4096, VCPUs: 2, DiskSize: 20,
		}},
		Kubernetes: config.KubernetesConfig{
			Clusters: []config.ClusterConfig{{Name: "cluster-1", CNI: config.CNIOVNKubernetes}},
		},
	}
	mode, err := cfg.GetDeploymentMode()
	require.NoError(t, err)
	require.Equal(t, config.VMDeploymentMode, mode)
}

func TestResolvePathRelativeToConfig(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "d", "cfg.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfg), 0o755))
	kc := filepath.Join(tmp, "d", "kc", "a.kubeconfig")
	require.NoError(t, os.MkdirAll(filepath.Dir(kc), 0o755))
	require.NoError(t, os.WriteFile(kc, []byte("x"), 0o600))

	got, err := resolvePathRelativeToConfig(cfg, "kc/a.kubeconfig")
	require.NoError(t, err)
	require.Equal(t, kc, got)
}

// Capture the actual file sent to the harness, including --tft-config runs.
type captureTFTExecutor struct {
	platform.CommandExecutor
	content []byte
	path    string
}

func (e *captureTFTExecutor) RunCmdInDir(_ log.Level, _, _ string, args ...string) error {
	e.path = args[1]
	var err error
	e.content, err = os.ReadFile(e.path)
	return err
}
func TestRunExternalProfileUsesVMNodesAndKubeconfig(t *testing.T) {
	t.Setenv("TFT_KUBECONFIG", "")
	cfg, err := config.LoadConfig("../../config-ovnk-offload.yaml")
	require.NoError(t, err)
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "tft.py"), nil, 0600))
	cfg.TrafficFlowTestsKubeconfig = filepath.Join(tmp, "host.kubeconfig")
	require.NoError(t, os.WriteFile(cfg.TrafficFlowTestsKubeconfig, []byte("test"), 0600))
	profile := filepath.Join(tmp, "profile.yaml")
	original := []byte(`tft:
- name: smoke
  connections:
  - server:
    - name: "@host-1"
    client:
    - name: "@host-2"
`)
	require.NoError(t, os.WriteFile(profile, original, 0600))
	e := &captureTFTExecutor{}
	require.NoError(t, Run(e, cfg, "../../config-ovnk-offload.yaml", RunOptions{TFTRepo: tmp, TFTConfig: profile}))
	require.Contains(t, string(e.content), "host-1-1")
	require.Contains(t, string(e.content), "host-2-1")
	require.Contains(t, string(e.content), cfg.TrafficFlowTestsKubeconfig)
	after, err := os.ReadFile(profile)
	require.NoError(t, err)
	require.Equal(t, original, after)
	_, err = os.Stat(e.path)
	require.True(t, os.IsNotExist(err), "temporary harness input should be removed")
}

func TestPortableProfilesAcrossBackends(t *testing.T) {
	for _, tt := range []struct{ path, first, second string }{
		{"../../config-kind-ovnk-offload.yaml", "dpu-sim-host-worker", "dpu-sim-host-worker2"},
		{"../../config-ovnk-offload.yaml", "host-1-1", "host-2-1"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			t.Setenv("TFT_KUBECONFIG", "")
			cfg, err := config.LoadConfig(tt.path)
			require.NoError(t, err)
			cfg.TrafficFlowTestsKubeconfig = filepath.Join(t.TempDir(), "kc")
			require.NoError(t, os.WriteFile(cfg.TrafficFlowTestsKubeconfig, []byte("test"), 0600))
			for _, p := range []string{"tft-overlay.yaml", "tft-no-overlay.yaml"} {
				b, err := prepareTFTConfig(cfg, tt.path, RunOptions{TFTConfig: filepath.Join("../../ci/tft-config", p)})
				require.NoError(t, err)
				require.Contains(t, string(b), tt.first)
				require.Contains(t, string(b), tt.second)
				require.NotContains(t, string(b), "@host-")
			}
		})
	}
}
func TestTFTRejectsUnresolvableAlias(t *testing.T) {
	t.Setenv("TFT_KUBECONFIG", "")
	cfg, err := config.LoadConfig("../../config-ovnk-offload.yaml")
	require.NoError(t, err)
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile.yaml")
	require.NoError(t, os.WriteFile(profile, []byte("kubeconfig: custom\ntft: [{connections: [{client: [{name: '@host-3'}]}]}]"), 0600))
	cfg.TrafficFlowTestsKubeconfig = ""
	cfg.Kubernetes.KubeconfigDir = filepath.Join(dir, "kubeconfig")
	require.NoError(t, os.MkdirAll(cfg.Kubernetes.KubeconfigDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Kubernetes.KubeconfigDir, "dpu-sim-host.kubeconfig"), []byte("test"), 0600))
	_, err = prepareTFTConfig(cfg, "unused", RunOptions{TFTConfig: profile, Cluster: "dpu-sim-host"})
	require.ErrorContains(t, err, "has 2 paired host nodes")
}

func TestTFTRejectsAmbiguousProfileKubeconfigAliases(t *testing.T) {
	t.Setenv("TFT_KUBECONFIG", "")
	cfg, err := config.LoadConfig("../../config-ovnk-offload.yaml")
	require.NoError(t, err)
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile.yaml")
	require.NoError(t, os.WriteFile(profile, []byte("kubeconfig: custom\ntft: [{connections: [{client: [{name: '@host-1'}]}]}]"), 0600))

	_, err = prepareTFTConfig(cfg, "unused", RunOptions{TFTConfig: profile})
	require.ErrorContains(t, err, "ambiguous for @host-N aliases")
}

func TestTFTAliasesFollowConfiguredKubeconfigOverClusterFlag(t *testing.T) {
	t.Setenv("TFT_KUBECONFIG", "")
	cfg, err := config.LoadConfig("../../config-ovnk-offload.yaml")
	require.NoError(t, err)
	cfg.TrafficFlowTestsKubeconfig = filepath.Join(t.TempDir(), "host.kubeconfig")
	require.NoError(t, os.WriteFile(cfg.TrafficFlowTestsKubeconfig, []byte("test"), 0600))
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile.yaml")
	require.NoError(t, os.WriteFile(profile, []byte("tft: [{connections: [{client: [{name: '@host-1'}]}]}]"), 0600))

	data, err := prepareTFTConfig(cfg, "unused", RunOptions{TFTConfig: profile, Cluster: "dpu-sim-dpu"})
	require.NoError(t, err)
	require.Contains(t, string(data), "host-1-1")
}
func TestTFTKeepsExplicitNodesAndExternalKubeconfig(t *testing.T) {
	cfg, err := config.LoadConfig("../../config-ovnk-offload.yaml")
	require.NoError(t, err)
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile.yaml")
	require.NoError(t, os.WriteFile(profile, []byte("kubeconfig: custom\ntft: [{connections: [{client: [{name: real-node}]}]}]"), 0600))
	b, err := prepareTFTConfig(cfg, "unused", RunOptions{TFTConfig: profile})
	require.NoError(t, err)
	require.Contains(t, string(b), filepath.Join(dir, "custom"))
	require.Contains(t, string(b), "real-node")
}

func TestExternalProfileWithoutAliasesNeedsNoSimulatorTopology(t *testing.T) {
	tmp := t.TempDir()
	profile := filepath.Join(tmp, "profile.yaml")
	require.NoError(t, os.WriteFile(profile, []byte(`kubeconfig: external.kubeconfig
tft:
- connections:
  - server:
    - name: external-worker-1
    client:
    - name: external-worker-2
`), 0o600))
	data, err := prepareTFTConfig(&config.Config{}, "", RunOptions{TFTConfig: profile})
	require.NoError(t, err)
	require.Contains(t, string(data), "external-worker-1")
	require.Contains(t, string(data), filepath.Join(tmp, "external.kubeconfig"))
}
