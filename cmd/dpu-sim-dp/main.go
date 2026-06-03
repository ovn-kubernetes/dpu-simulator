// dpu-sim-dp is a simulated Kubernetes device plugin that advertises
// host-to-DPU data interfaces as allocatable pseudo-VF resources.
// It is deployed as a DaemonSet on DPU-host nodes so that OVN-Kubernetes
// can allocate management-port and pod VFs through the standard device
// plugin mechanism.
//
// One gRPC server is started per resource pool built from MGMT_PORT_VFS_COUNT.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/deviceplugin"
	"golang.org/x/sync/errgroup"
	"k8s.io/klog/v2"
)

func main() {
	klog.InitFlags(nil)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	mgmtPortVFsCount := deviceplugin.MgmtPortVFsCountFromEnv()
	pools := deviceplugin.BuildResourcePools(mgmtPortVFsCount)
	klog.Infof("Configured mgmt_port_vfs_count=%d", mgmtPortVFsCount)

	for _, pool := range pools {
		klog.Infof("Configured pool: resource=%s socket=%s selector=%s", pool.ResourceName, pool.SocketName, pool.MatcherDescription())
	}

	g, ctx := errgroup.WithContext(ctx)
	for _, pool := range pools {
		g.Go(func() error {
			plugin := NewDevicePlugin(pool)
			if err := plugin.Run(ctx); err != nil {
				return fmt.Errorf("pool %s: %w", pool.ResourceName, err)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		klog.Errorf("Device plugin exited with error: %v", err)
		os.Exit(1)
	}
}
