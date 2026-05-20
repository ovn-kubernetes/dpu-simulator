package kind

import (
	"fmt"
	"strings"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

// CleanupAll performs comprehensive cleanup of all resources and attempts to
// continue cleanup even if errors occurs.
func (m *KindManager) CleanupAll(cfg *config.Config) error {
	errors := make([]string, 0)

	for _, cluster := range cfg.Kubernetes.Clusters {
		if err := m.DeleteCluster(cluster.Name); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to delete cluster %s: %v", cluster.Name, err))
		}
	}

	if cfg.IsOffloadDPU() {
		cmdExec := platform.NewLocalExecutor()
		if err := cmdExec.RunCmd(log.LevelDebug, m.containerBin, "network", "rm", cfg.DPUKindGatewayNetworkName()); err != nil {
			if isMissingContainerNetworkError(err) {
				log.Debug("DPU gateway network %s is already removed: %v", cfg.DPUKindGatewayNetworkName(), err)
			} else {
				errors = append(errors, fmt.Sprintf("Failed to remove DPU gateway network %s: %v", cfg.DPUKindGatewayNetworkName(), err))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errors, "; "))
	}

	return nil
}

func isMissingContainerNetworkError(err error) bool {
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "no such network") || strings.Contains(errText, "network not found")
}
