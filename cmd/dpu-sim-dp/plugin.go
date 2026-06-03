// Package main implements a simulated Kubernetes device plugin that exposes
// host-to-DPU data interfaces as allocatable resources. Multiple resource
// pools are supported — each pool gets its own gRPC socket and kubelet
// registration so that OVN-Kubernetes DPU-host mode can allocate management-
// port and pod VFs independently through the standard device plugin mechanism.
//
// Pools are built from pkg/deviceplugin.BuildResourcePools.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/deviceplugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const dpDeviceInfoPath = "/var/run/k8s.cni.cncf.io/devinfo/dp"

// DpuSimDevicePlugin implements the Kubernetes device plugin API for a single resource pool.
type DpuSimDevicePlugin struct {
	pluginapi.UnimplementedDevicePluginServer
	pool       deviceplugin.ResourcePool
	socketPath string
	server     *grpc.Server
	devices    []*pluginapi.Device
}

func NewDevicePlugin(pool deviceplugin.ResourcePool) *DpuSimDevicePlugin {
	return &DpuSimDevicePlugin{
		pool:       pool,
		socketPath: filepath.Join(pluginapi.DevicePluginPath, pool.SocketName),
	}
}

func (p *DpuSimDevicePlugin) discoverDevices() error {
	ifaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("failed to list interfaces: %w", err)
	}

	p.devices = nil
	for _, iface := range ifaces {
		if p.pool.MatchesIface(iface.Name) {
			p.devices = append(p.devices, &pluginapi.Device{
				ID:     iface.Name,
				Health: pluginapi.Healthy,
			})
			klog.Infof("[%s] discovered device: %s", p.pool.ResourceName, iface.Name)
		}
	}

	if len(p.devices) == 0 {
		return fmt.Errorf("[%s] no interfaces matching %s found", p.pool.ResourceName, p.pool.MatcherDescription())
	}
	klog.Infof("[%s] discovered %d device(s)", p.pool.ResourceName, len(p.devices))
	return nil
}

// Run discovers devices, starts the gRPC server, and registers with kubelet.
// It blocks until ctx is cancelled.
func (p *DpuSimDevicePlugin) Run(ctx context.Context) error {
	if err := p.discoverDevices(); err != nil {
		return err
	}

	// TODO: Reclaim stale device-info files only after reconciling live
	// allocations, so files still referenced by active pods are preserved.
	if err := p.writeDeviceInfoFiles(); err != nil {
		return err
	}

	if err := p.startServer(); err != nil {
		return fmt.Errorf("[%s] failed to start gRPC server: %w", p.pool.ResourceName, err)
	}

	if err := p.registerWithKubelet(); err != nil {
		return fmt.Errorf("[%s] failed to register with kubelet: %w", p.pool.ResourceName, err)
	}

	klog.Infof("[%s] registered with kubelet (devices=%d)", p.pool.ResourceName, len(p.devices))

	<-ctx.Done()
	p.server.GracefulStop()
	os.Remove(p.socketPath)
	return nil
}

func (p *DpuSimDevicePlugin) startServer() error {
	os.Remove(p.socketPath)

	listener, err := net.Listen("unix", p.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", p.socketPath, err)
	}

	p.server = grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(p.server, p)

	go func() {
		if err := p.server.Serve(listener); err != nil {
			klog.Errorf("gRPC server exited: %v", err)
		}
	}()

	// Wait for the socket to become ready.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "unix://"+p.socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock())
	if err != nil {
		return fmt.Errorf("gRPC server did not become ready: %w", err)
	}
	conn.Close()
	return nil
}

func (p *DpuSimDevicePlugin) registerWithKubelet() error {
	conn, err := grpc.Dial("unix://"+pluginapi.KubeletSocket,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("failed to connect to kubelet: %w", err)
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	_, err = client.Register(context.Background(), &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     p.pool.SocketName,
		ResourceName: p.pool.ResourceName,
	})
	return err
}

func (p *DpuSimDevicePlugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{}, nil
}

func (p *DpuSimDevicePlugin) ListAndWatch(_ *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	if err := stream.Send(&pluginapi.ListAndWatchResponse{Devices: p.devices}); err != nil {
		return err
	}

	// Block until the stream context is done; devices are static.
	<-stream.Context().Done()
	return nil
}

func (p *DpuSimDevicePlugin) Allocate(_ context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	resp := &pluginapi.AllocateResponse{}
	for _, creq := range req.ContainerRequests {
		ids := strings.Join(creq.DevicesIds, ",")
		klog.Infof("[%s] allocating devices: %s", p.pool.ResourceName, ids)
		for _, id := range creq.DevicesIds {
			if err := p.writeDeviceInfoFile(id); err != nil {
				return nil, err
			}
		}
		resp.ContainerResponses = append(resp.ContainerResponses, &pluginapi.ContainerAllocateResponse{
			Envs: map[string]string{
				p.pool.EnvVarName: ids,
			},
		})
	}
	return resp, nil
}

func (p *DpuSimDevicePlugin) writeDeviceInfoFiles() error {
	for _, device := range p.devices {
		if err := p.writeDeviceInfoFile(device.ID); err != nil {
			return err
		}
	}
	return nil
}

func (p *DpuSimDevicePlugin) writeDeviceInfoFile(deviceID string) error {
	if err := os.MkdirAll(dpDeviceInfoPath, 0755); err != nil {
		return fmt.Errorf("[%s] failed to create device info directory: %w", p.pool.ResourceName, err)
	}

	info := map[string]string{
		"version": "1.1.0",
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("[%s] failed to marshal device info for %s: %w", p.pool.ResourceName, deviceID, err)
	}
	data = append(data, '\n')

	path := filepath.Join(dpDeviceInfoPath, deviceInfoFileName(p.pool.ResourceName, deviceID))
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("[%s] failed to write device info for %s: %w", p.pool.ResourceName, deviceID, err)
	}
	klog.Infof("[%s] wrote device info for %s to %s", p.pool.ResourceName, deviceID, path)
	return nil
}

func deviceInfoFileName(resourceName, deviceID string) string {
	return fmt.Sprintf("%s-%s-device.json",
		strings.ReplaceAll(resourceName, "/", "-"),
		strings.ReplaceAll(deviceID, "/", "-"))
}

func (p *DpuSimDevicePlugin) GetPreferredAllocation(context.Context, *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	return &pluginapi.PreferredAllocationResponse{}, nil
}

func (p *DpuSimDevicePlugin) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}
