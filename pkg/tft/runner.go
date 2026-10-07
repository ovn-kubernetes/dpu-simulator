// Package tft runs the kubernetes-traffic-flow-tests harness against dpu-sim Kubernetes
// clusters (Kind or VM/bare-metal deployments that use the same kubeconfig layout).
package tft

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/k8s"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

const (
	// DefaultTFTRepoURL is the default URL for the kubernetes-traffic-flow-tests repository.
	DefaultTFTRepoURL = "https://github.com/ovn-kubernetes/kubernetes-traffic-flow-tests"
	// DefaultTFTDirName is the directory name used when auto-cloning the TFT repo.
	DefaultTFTDirName = "kubernetes-traffic-flow-tests"
)

// RunOptions controls how tft.py is invoked.
type RunOptions struct {
	TFTRepo   string
	Python    string
	TFTConfig string
	Cluster   string
	Check     bool
}

// ResolveTFTRepo returns an absolute path to the kubernetes-traffic-flow-tests checkout.
// When repoFlag is set (via --tft-repo-path) the directory is used as-is.
// Otherwise it checks <project-root>/kubernetes-traffic-flow-tests and clones
// the repository if missing.
func ResolveTFTRepo(cmdExec platform.CommandExecutor, repoFlag string) (string, error) {
	if repoFlag != "" {
		return verifyTFTRepo(repoFlag)
	}

	projectRoot, err := platform.GetProjectRoot()
	if err != nil {
		return "", fmt.Errorf("failed to get project root: %w", err)
	}

	tftPath := filepath.Join(projectRoot, DefaultTFTDirName)

	if abs, err := verifyTFTRepo(tftPath); err == nil {
		log.Debug("kubernetes-traffic-flow-tests found at %s", abs)
		return abs, nil
	}

	// Remove only an empty (or .git-only) leftover directory so clone can succeed.
	exists, err := cmdExec.FileExists(tftPath)
	if err != nil {
		return "", fmt.Errorf("failed to check kubernetes-traffic-flow-tests path: %w", err)
	}
	if exists {
		names, err := cmdExec.ReadDirNames(tftPath)
		if err != nil {
			return "", fmt.Errorf("failed to read kubernetes-traffic-flow-tests directory %s: %w", tftPath, err)
		}
		meaningful := platform.MeaningfulDirEntries(names)
		if len(meaningful) == 0 {
			log.Info("kubernetes-traffic-flow-tests directory exists but is empty (or only .git), removing before clone...")
			if err := cmdExec.RemoveAll(tftPath); err != nil {
				return "", fmt.Errorf("failed to remove empty kubernetes-traffic-flow-tests directory: %w", err)
			}
		} else {
			tftPy := filepath.Join(tftPath, "tft.py")
			return "", fmt.Errorf("kubernetes-traffic-flow-tests directory %q exists but %s is missing (invalid or partial checkout; directory is not empty — remove or fix the tree and retry)", tftPath, tftPy)
		}
	}

	log.Info("kubernetes-traffic-flow-tests not found, cloning from %s...", DefaultTFTRepoURL)
	if err := cmdExec.RunCmdInDir(log.LevelInfo, projectRoot, "git", "clone", DefaultTFTRepoURL, tftPath); err != nil {
		return "", fmt.Errorf("failed to clone kubernetes-traffic-flow-tests repository: %w", err)
	}

	log.Info("✓ kubernetes-traffic-flow-tests cloned to %s", tftPath)
	return tftPath, nil
}

// ValidateTFTRepoPath returns nil if path is empty (default checkout / clone applies).
// If path is non-empty, it must be a directory containing tft.py.
func ValidateTFTRepoPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	_, err := verifyTFTRepo(path)
	return err
}

// verifyTFTRepo verifies that the directory contains a tft.py file. This assumes that we will never rename the
// main entrypoint python script.
func verifyTFTRepo(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(abs, "tft.py")); err != nil {
		return "", fmt.Errorf("tft.py not found in %s: %w", abs, err)
	}
	return abs, nil
}

// tftDefaultCluster picks the kubeconfig cluster for TFT when none is specified:
// the first kubernetes.clusters entry that does not contain DPU-type nodes (tenant / host side).
// If every listed cluster has DPU workers, falls back to GetDPUHostClusterName(), then the first cluster.
func tftDefaultCluster(cfg *config.Config) (string, error) {
	if len(cfg.Kubernetes.Clusters) == 0 {
		return "", fmt.Errorf("no kubernetes.clusters in config and kubeconfig not set")
	}
	for _, cl := range cfg.Kubernetes.Clusters {
		if !cfg.IsDPUCluster(cl.Name) {
			return cl.Name, nil
		}
	}
	if host := cfg.GetDPUHostClusterName(); host != "" {
		return host, nil
	}
	return cfg.Kubernetes.Clusters[0].Name, nil
}

// ResolveKubeconfigPath picks the absolute kubeconfig path for TFT.
func ResolveKubeconfigPath(cfg *config.Config, dpuSimConfigPath, clusterOverride string) (string, error) {
	// CI sets TFT_KUBECONFIG to the host-cluster file written at deploy time.
	// Honor it first so callers do not depend on process cwd or a kubeconfig:
	// path relative to the dpu-sim YAML (ci/config-*.yaml would otherwise
	// resolve to ci/kubeconfig/...).
	if envPath := strings.TrimSpace(os.Getenv("TFT_KUBECONFIG")); envPath != "" {
		absPath, err := filepath.Abs(envPath)
		if err != nil {
			return "", fmt.Errorf("TFT_KUBECONFIG: %w", err)
		}
		if err := k8s.RequireKubeconfigFile(absPath, ""); err != nil {
			return "", fmt.Errorf("%w (from TFT_KUBECONFIG); fix the path or run dpu-sim deploy", err)
		}
		return absPath, nil
	}

	if strings.TrimSpace(cfg.TrafficFlowTestsKubeconfig) != "" {
		if c := strings.TrimSpace(clusterOverride); c != "" {
			log.Warn("tft: --cluster %q is ignored because kubeconfig: is set in the dpu-sim config (%q)",
				c, strings.TrimSpace(cfg.TrafficFlowTestsKubeconfig))
		}
		path, err := resolvePathRelativeToConfig(dpuSimConfigPath, cfg.TrafficFlowTestsKubeconfig)
		if err != nil {
			return "", err
		}
		if err := k8s.RequireKubeconfigFile(path, ""); err != nil {
			return "", fmt.Errorf("%w (from kubeconfig: in dpu-sim config); fix the path or run dpu-sim deploy", err)
		}
		return path, nil
	}

	cluster := strings.TrimSpace(clusterOverride)
	if cluster == "" {
		var err error
		cluster, err = tftDefaultCluster(cfg)
		if err != nil {
			return "", err
		}
	}

	rel := k8s.GetKubeconfigPath(cluster, cfg.Kubernetes.GetKubeconfigDir())
	absPath, err := filepath.Abs(rel)
	if err != nil {
		return "", fmt.Errorf("kubeconfig path: %w", err)
	}
	if err := k8s.RequireKubeconfigFile(absPath, cluster); err != nil {
		return "", err
	}
	return absPath, nil
}

func resolvePathRelativeToConfig(dpuSimConfigPath, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	base := filepath.Dir(dpuSimConfigPath)
	if base == "" || base == "." {
		return filepath.Abs(p)
	}
	return filepath.Abs(filepath.Join(base, p))
}

// HasEmbeddedTFT reports whether the dpu-sim config carries a tft: section for the harness.
func HasEmbeddedTFT(cfg *config.Config) bool {
	return cfg.TFT != nil && cfg.TFT.Node() != nil
}

// Run executes tft.py. Either opts.TFTConfig is set, or cfg must include a tft: block.
func Run(cmdExec platform.CommandExecutor, cfg *config.Config, dpuSimConfigPath string, opts RunOptions) error {
	if _, err := cfg.GetDeploymentMode(); err != nil {
		return fmt.Errorf("dpu-sim tft needs a valid deployment config (kind: or vms:/baremetal:): %w", err)
	}
	repo, err := ResolveTFTRepo(cmdExec, opts.TFTRepo)
	if err != nil {
		return err
	}
	py, err := PythonForTFTRun(repo, opts.Python)
	if err != nil {
		return fmt.Errorf("tft python: %w", err)
	}

	data, err := prepareTFTConfig(cfg, dpuSimConfigPath, opts)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "dpu-sim-tft-*.yaml")
	if err != nil {
		return fmt.Errorf("temp tft config: %w", err)
	}
	tftYAML := f.Name()
	defer func() { _ = os.Remove(tftYAML) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	log.Info("Running kubernetes-traffic-flow-tests from %s", repo)
	log.Info("TFT config: %s", tftYAML)
	log.Info("Python: %s", py)

	args := []string{"tft.py", tftYAML}
	if opts.Check {
		args = append(args, "--check")
	}
	if err := cmdExec.RunCmdInDir(log.LevelInfo, repo, py, args...); err != nil {
		return fmt.Errorf("tft.py: %w", err)
	}
	return nil
}
