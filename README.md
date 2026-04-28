# DPU Simulator - VM and Container-based Kubernetes + OVN-Kubernetes CNI Environment

This project automates the deployment of DPU simulation environments using either **VMs (libvirt)** or **containers (Kind)**, pre-configured with Kubernetes and OVN-Kubernetes (or other CNIs) for container networking experiments/development/CI/CD.

DPUs are being used in data centers to accelerate different workloads such as AI (Artificial Intelligence), NFs (Network Functions) and many use cases. This DPU simulation's goal is to bring the DPU into developer's hands without needing the hardware. DPU hardware has limitations such as ease of provisioning, hardware availability, cost, embedded CPU capacity, and others, the DPU simulation tools here using Virtual Machines or Containers should lower the barrier of entry to move fast in developing features in Kubernetes, CNIs, APIs, etc... The second objective is to use this simulation in upstream CI/CD for CNIs that support offloading to DPUs such as OVN-Kubernetes

These are the list of DPUs that this simulation will try to emulate:
- NVIDIA BlueField 3
- Marvell Octeon 10
- Intel NetSec Accelerator
- Intel IPU

All these DPUs have common simularities, some we can emulate better than others. As this DPU simulation project grows there would a increased interest and need to simulate the hardware closely (e.g. eSwitch) in QEMU drivers.

## Status: 🚧 Active Development
 - `dpu-sim` is functional for VM & Kind mode.
 - `vmctl` is functional for managing VMs created by dpu-sim.

## Features

### Core Features
- 🚀 **Multiple deployment modes**: VMs (libvirt) or Containers (Kind)
- ☸️ Kubernetes (kubeadm, kubelet, kubectl) pre-installed
- 🔀 OVN-Kubernetes or Flannel CNI support
- ⚡ **OVN-Kubernetes DPU offloading**: optional split topology (DPU-host vs DPU cluster) via `kubernetes.offload_dpu`; see `config-ovnk-offload.yaml` (VM) and `config-kind-ovnk-offload.yaml` (Kind)
- 🌐 Multiple network support (NAT, Host-To-DPU interfaces, Layer 2 Bridge)
- ✅ Automatic cluster setup and CNI installation
- 🧹 Cleanup scripts for both modes

### VM Mode Features
- 🔌 Configurable NIC models (virtio, igb)
- 🖥️ Q35 machine type with PCIe and IOMMU support (SR-IOV ready)
- 🔑 SSH key-based authentication
- 💻 Easy VM access via SSH and console
- 🎛️ Full VM lifecycle management (start, stop, reboot)
- 🔀 Open vSwitch (OVS) for Host-To-DPU networking

### Kind Mode Features
- ⚡ **Fast iteration** - clusters deploy in seconds
- 🐳 Uses Docker containers instead of VMs
- 💾 Lower resource usage than VMs
- 🔄 Easy cluster recreation for testing

## Prerequisites

### System Requirements
- Fedora/RHEL/CentOS Linux
- Golang compiler
- Make
- **For VM Mode**: KVM/QEMU virtualization support, at least 12GB RAM, 100GB disk
- **For Kind Mode**: Container support, at least 8GB RAM

### Dependencies
Runtime dependencies are automatically installed by dpu-sim. For example the dpu-sim binary will output the following if all depencies are meet on the system:

Separate dependencies are checked based on whether the provided configuration deploys VM or Kind mode.

For VM-based deployments:
```bash
=== Checking Dependencies ===
✓ Detected Linux distribution: rhel 9.6 (package manager: dnf, architecture: x86_64)
✓ wget is installed
✓ pip3 is installed
✓ jinjanator is installed
✓ git is installed
✓ openvswitch is installed
✓ libvirt is installed
✓ qemu-kvm is installed
✓ qemu-img is installed
✓ libvirt-devel is installed
✓ virt-install is installed
✓ genisoimage is installed
✓ aarch64-uefi-firmware is installed
✓ All dependencies are available
```

For Kind-based deployments:
```bash
=== Checking Dependencies ===
✓ Detected Linux distribution: rhel 9.6 (package manager: dnf, architecture: x86_64)
✓ wget is installed
✓ pip3 is installed
✓ jinjanator is installed
✓ git is installed
✓ openvswitch is installed
✓ kubectl is installed
✓ Container Runtime is installed
✓ kind is installed
✓ All dependencies are available
```

### Required Packages

The dpu-sim should install all dependecies by detecting the system's Linux distribution. However some distributions require enabling subscriptions to allow the installation of some packages. This is outside the scope of dpu-sim; however depending on the distribution, dpu-sim will try to enable repositories.

### Required Services for VM-based deployments

Although dpu-sim tries to install dependencies, the user may be required to start required services for VM-based deployments. This can potentially go away once dpu-sim handles these required services in its entirety.

```bash
# Start and enable libvirt sockets
sudo systemctl start virtqemud.socket virtnetworkd.socket
sudo systemctl enable virtqemud.socket virtnetworkd.socket

# Start and enable Open vSwitch
sudo systemctl start openvswitch
sudo systemctl enable openvswitch

# Verify services are active
sudo systemctl status virtqemud.socket
sudo systemctl status virtnetworkd.socket
sudo systemctl status openvswitch

# Add user to libvirt group
sudo usermod -a -G libvirt $USER
newgrp libvirt
```

### Required SSH Key Setup for VM-based deployments

Generate SSH keys if you don't have them:

```bash
ssh-keygen -t rsa -b 4096 -f ~/.ssh/id_rsa
```

### Compiling Binaries

In order to use the dpu-sim binary, the binaries must be built. To compile the GO binaries, golang compilers must be installed.

```bash
$ go version
go version go1.25.3 (Red Hat 1.25.3-1.el9_7) linux/amd64
$ make build
Building binaries...
  Building dpu-sim...
  Building vmctl...
Build complete! Binaries are in bin/
$ ls -lh bin/
total 87M
-rwxr-xr-x. 1 root root 55M Apr 27 17:40 dpu-sim
-rwxr-xr-x. 1 root root 33M Apr 27 17:40 vmctl
```
### Makefile Commands

```bash
DPU Simulator - Makefile commands:
  build                Build all binaries
  test                 Run tests
  test-coverage        Run tests and show coverage
  test-integration     Run integration tests
  clean                Clean build artifacts
  install              Install binaries to $GOPATH/bin
  fmt                  Format code
  vet                  Run go vet
  lint                 Run golangci-lint
  check                Run all checks (fmt, vet, test)
  build-all            Cross-compile for Linux (amd64, arm64) and macOS
  deps                 Download dependencies
  help                 Display this help message
  tft-venv             Create TFT Python venv (needs Python >= 3.11; pass PYTHON= if needed)
  tft-run              Run TFT via dpu-sim (set CONFIG=path/to.yaml; uses submodule .tft-venv if present)
```

## Configuration

The simulator supports two deployment modes, configured via different sections in the YAML config file:

- **VM Mode**: Uses `vms` section (libvirt-based VMs)
- **Kind Mode**: Uses `kind` section (Docker containers)

### Kind Mode Configuration (config-kind.yaml)

Kind mode supports **two clusters** (host and DPU), similar to the VM approach. One Kind cluster is created per entry in `kubernetes.clusters`. Each node must specify which cluster it belongs to and is identified by a **node label** (`dpu-sim.org/node-name`) because Kind does not support renaming nodes.

**Node fields:**

| Field         | Required | Description |
|---------------|----------|-------------|
| `name`        | Yes      | Logical node name; applied as label `dpu-sim.org/node-name` on the Kind node. |
| `k8s_cluster` | Yes      | Cluster name from `kubernetes.clusters` this node belongs to. |
| `k8s_role`    | Yes      | `control-plane` or `worker`. |
| `type`        | No       | For workers: `host` (host side) or `dpu` (DPU side). Omit for control-plane. |
| `host`        | Yes*     | For `type: dpu` only: `name` of the `host` node this DPU is paired with. |

The following is an example with two clusters (host cluster and DPU cluster) and two host–DPU pairs. Edit `config-kind.yaml` to customize your deployment:

```yaml
networks:
  - name: "host-to-dpu-link"
    type: "HostToDpu"
    num_pairs: 16

kind:
  nodes:
    - name: "control-plane-host"
      k8s_role: "control-plane"
      k8s_cluster: "dpu-sim-host-kind"
    - name: "control-plane-dpu"
      k8s_role: "control-plane"
      k8s_cluster: "dpu-sim-dpu-kind"
    - name: "host-1-1"
      type: host
      k8s_role: "worker"
      k8s_cluster: "dpu-sim-host-kind"
    - name: "dpu-1-1"
      type: dpu
      k8s_role: "worker"
      k8s_cluster: "dpu-sim-dpu-kind"
      host: "host-1-1"
    - name: "host-2-1"
      type: host
      k8s_role: "worker"
      k8s_cluster: "dpu-sim-host-kind"
    - name: "dpu-2-1"
      type: dpu
      k8s_role: "worker"
      k8s_cluster: "dpu-sim-dpu-kind"
      host: "host-2-1"
...
```

To look up a node by its config name after deployment, use the label: `kubectl get nodes -l dpu-sim.org/node-name=host-1-1`.

### VM Mode Configuration (config.yaml)

Edit `config.yaml` to customize your deployment:

```yaml
networks:
  # Management network with internet access
  - name: "mgmt-network"
    type: "mgmt"
    bridge_name: "virbr-mgmt"
    gateway: "192.168.120.1"
    subnet_mask: "255.255.255.0"
    dhcp_start: "192.168.120.10"
    dhcp_end: "192.168.120.100"
    mode: "nat"
    nic_model: "virtio"  # virtio for networks because it is the fastest and least resource intensive
    attach_to: "any"  # Attach to all VMs: "dpu", "host", or "any"

  - name: "ovn-network"
    type: "k8s"
    bridge_name: "ovn"
    gateway: "192.168.123.1"
    subnet_mask: "255.255.255.0"
    dhcp_start: "192.168.123.50"
    dhcp_end: "192.168.123.100"
    mode: "nat"
    nic_model: "virtio"
    use_ovs: false
    attach_to: "any"

  # Pure Layer 2 data network with OVS (no IP/DHCP)
  - name: "data-l2-network"
    type: "layer2"
    bridge_name: "ovs-data"
    mode: "l2-bridge"
    nic_model: "virtio"
    use_ovs: true  # Use Open vSwitch (supports OpenFlow, flow tables, etc.)
    attach_to: "dpu"  # Attach to all VMs: "dpu", "host", or "any"

  - name: "host-to-dpu-link"
    type: "HostToDpu"
    num_pairs: 16
    nic_model: "virtio"

vms:
  - name: "master-1"
    type: "host"
    k8s_cluster: "cluster-1"
    k8s_role: "master"
    k8s_node_mac: "52:54:00:00:01:11"
    k8s_node_ip: "192.168.123.11"
    memory: 4096  # MB
    vcpus: 2
    disk_size: 20  # GB

  - name: "master-2"
    type: "host"
    k8s_cluster: "cluster-2"
    k8s_role: "master"
    k8s_node_mac: "52:54:00:00:02:11"
    k8s_node_ip: "192.168.123.21"
    memory: 4096  # MB
    vcpus: 2
    disk_size: 20  # GB

  - name: "host-1-1"
    type: "host"
    k8s_cluster: "cluster-1"
    k8s_role: "worker"
    k8s_node_mac: "52:54:00:00:01:12"
    k8s_node_ip: "192.168.123.12"
    memory: 2048  # MB
    vcpus: 2
    disk_size: 20  # GB

  - name: "dpu-1-1"
    type: "dpu"
    k8s_cluster: "cluster-2"
    k8s_role: "worker"
    k8s_node_mac: "52:54:00:00:02:12"
    k8s_node_ip: "192.168.123.22"
    host: "host-1-1"
    memory: 2048  # MB
    vcpus: 2
    disk_size: 20  # GB

  - name: "host-2-1"
    type: "host"
    k8s_cluster: "cluster-1"
    k8s_role: "worker"
    k8s_node_mac: "52:54:00:00:01:13"
    k8s_node_ip: "192.168.123.13"
    memory: 2048  # MB
    vcpus: 2
    disk_size: 20  # GB

  - name: "dpu-2-1"
    type: "dpu"
    k8s_cluster: "cluster-2"
    k8s_role: "worker"
    k8s_node_mac: "52:54:00:00:02:13"
    k8s_node_ip: "192.168.123.23"
    host: "host-2-1"
    memory: 2048  # MB
    vcpus: 2
    disk_size: 20  # GB

operating_system:
  # Download from https://download.fedoraproject.org/pub/fedora/linux/releases/
  image_url: https://mirror.xenyth.net/fedora/linux/releases/43/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-43-1.6.x86_64.qcow2
  image_name: "Fedora-x86_64.qcow2"

ssh:
  user: "root"
  key_path: "~/.ssh/id_rsa"
  password: "redhat"  # Default password for console/SSH access

kubernetes:
  version: "1.33"
  kubeconfig_dir: "kubeconfig"
  clusters:
    - name: "cluster-1"
      pod_cidr: "10.244.0.0/16"
      service_cidr: "10.245.0.0/16"
      cni: "ovn-kubernetes"
      addons:
        - "multus"
        - "whereabouts"
        - "cert-manager"
    - name: "cluster-2"
      pod_cidr: "10.246.0.0/16"
      service_cidr: "10.247.0.0/16"
      cni: "ovn-kubernetes"

registry:
  containers:
    - name: "ovn-kube"
      cni: "ovn-kubernetes"
      tag: "ovn-kube:dpu-sim"
```

### Network Types

Network types change the behaviour of dpu-sim on how they treat the network. For example "k8s" network shouldn't be used to access machines, rather the "mgmt" network should be used (more stable/non-changing)

- **`mgmt`**: A non-changing network to provide SSH access to the machine
- **`k8s`**: A network that the CNI would have access to. For example OVN-Kubernetes would have control of this network and it's interfaces.
- **`layer2`**: A network that is layer 2 connection between 2 machines. Currently dpu-sim does not modify this network beyond configuring it.

### Network Modes

- **`nat`**: VMs can communicate with each other AND access the internet via NAT (requires gateway, subnet_mask, dhcp_start, dhcp_end)
- **`l2-bridge`**: Pure Layer 2 bridge - VMs connected like a switch, no IP/DHCP management (configure IPs manually in VMs)
  - Set `use_ovs: true` to use Open vSwitch instead of Linux bridge
  - OVS provides: OpenFlow support, flow tables, VLAN tagging, port mirroring, QoS, and more

### Network Attachment

The `attach_to` field controls which VM types a network should attach to:

- **`any`** (default): Attach to all VMs regardless of type
- **`host`**: Only attach to VMs with `type: host`
- **`dpu`**: Only attach to VMs with `type: dpu`

Example use case: You might want a management network attached to all VMs, but a specific data plane network only attached to DPU VMs.

### NIC Models

- **`virtio`**: High-performance paravirtualized NIC (recommended for management)
- **`igb`**: Intel 82576 Gigabit Ethernet emulation (good for testing Intel drivers)
- **`e1000`**: Intel PRO/1000 emulation (widely compatible)
- **`e1000e`**: Intel 82574 emulation (newer than e1000)
- **`rtl8139`**: Realtek 8139 emulation

### VM Architecture

All VMs use the **Q35 machine type** which provides:
- **PCIe bus**
- **IOMMU support** (Intel VT-d emulation)
- **SR-IOV ready** architecture

This makes the VMs suitable for:
- Testing SR-IOV devices
- DPU emulation by interconnecting VMs with networks (OvS or Linux Bridge)
- Testing Kubernetes features with emulating hardware

### Kubernetes

Kubernetes is the choice for orchestrating DPU deployment. Hence kubernetes installation and usage is assumed. Although you might choose to simulate DPUs without Kubernetes, which currently means to pass the `--skip-k8s` flag to dpu-sim (Currently only VM mode supports this).

If Kubernetes is needed then by default dpu-sim will perform those operations automatically.

Each VM must specify which cluster it belongs to using the `k8s_cluster` field, which references a cluster name defined in the `kubernetes.clusters` section.

Each VM in `config.yaml` must have a `k8s_role` field with two supported values:
- **master**: Kubernetes control plane node
- **worker**: Kubernetes worker node

Everything Kubernetes related is in the `kubernetes` section. By default version `1.33 Kubernetes` is used however this can be overwritten in the `kubernetes.version` field. The resulting config files are generated and written into the kubeconfig directory by default, but this can be overwritten with `kubeconfig_dir`. Each cluster definition includes:
- **name**: Unique identifier for the cluster
- **pod_cidr**: Default is 10.244.0.0/16. This is the custom pod network CIDR
- **service_cidr**: Default is 10.245.0.0/16. This is the custom service CIDR.
- **cni**: Selects which CNI should be used in the cluster such as ovn-kubernetes
- **addons**: Optional ordered list of additional components to install (currently `multus`, `whereabouts`, `cert-manager`)

**OVN-Kubernetes DPU offloading** (`kubernetes.offload_dpu: true`): deploys OVN-Kubernetes in DPU-host mode on the cluster whose workers are host nodes, and in DPU mode on the cluster whose workers are DPU nodes (the DPU cluster is whichever `kubernetes.clusters` entry contains VMs or Kind nodes with `type: dpu`). When the host cluster uses OVN-Kubernetes, the DPU cluster can still use another primary CNI (for example Flannel) while OVN-Kubernetes is installed there in DPU mode so the offload path is wired correctly. Example configs: `config-ovnk-offload.yaml` (VM mode) and `config-kind-ovnk-offload.yaml` (Kind mode). Single-cluster host+DPU in one Kubernetes cluster is not supported for this topology yet.

Multiple cluster configuration example:
```yaml
kubernetes:
  version: "1.33"
  kubeconfig_dir: "kubeconfig"
  clusters:
    - name: "cluster-1"
      pod_cidr: "10.244.0.0/16" # First cluster pod network
      service_cidr: "10.245.0.0/16"
      cni: "ovn-kubernetes"
      addons:
        - "multus"
        - "whereabouts"
        - "cert-manager"
    - name: "cluster-2"
      pod_cidr: "10.246.0.0/16" # Second cluster pod network
      service_cidr: "10.247.0.0/16"
      cni: "ovn-kubernetes"

vms:
  - name: "master-1"
    k8s_cluster: "cluster-1"
    k8s_role: "master"
    ...

  - name: "master-2"
    k8s_cluster: "cluster-2"
    k8s_role: "master"
    ...

  - name: "host-1"
    k8s_cluster: "cluster-1"
    k8s_role: "worker"
    ...

  - name: "dpu-1"
    k8s_cluster: "cluster-2"
    k8s_role: "worker"
    ...
```

### Local Registry and CNI Image Builds

When developing or testing CNI changes, you can configure dpu-sim to build CNI container images from source and serve them through a local Docker registry. This works for both VM and Kind deployment modes.

#### Configuration

Add a `registry` section to your config file:

```yaml
registry:
  enabled: true
```

This enables an empty local registry (no CNI image builds), useful when you
want to push your own images (for example dpu-operator images).

For hybrid setups, you can override which registry endpoints nodes trust as
insecure HTTP registries:

```yaml
registry:
  enabled: true
  insecure_endpoints:
    - "172.22.1.100:5000"
    - "192.168.120.1:5000"
```

To also build/push CNI images from source, add `containers` entries:

```yaml
registry:
  enabled: true
  containers:
    - name: "ovn-kube"
      cni: "ovn-kubernetes"
      tag: "ovn-kube:dpu-sim"
```

To disable registry management entirely:

```yaml
registry:
  enabled: false
```

Each entry under `containers` defines an image to build:
- **name**: Human-readable identifier for the build
- **cni**: Which CNI's source code to compile (currently `ovn-kubernetes` is supported)
- **tag**: The `name:tag` used when pushing to the local registry (e.g. `ovn-kube:dpu-sim`)

#### How It Works

When `registry.enabled` is true (or omitted), dpu-sim automatically:

1. **Starts a local Docker registry** (`registry:2`) on port 5000
2. **Configures nodes to pull from the registry**:
   - **Kind mode**: Containerd on each node is configured to redirect `localhost:5000` pulls to the registry container on the Docker network
   - **VM mode**: CRI-O on each node is configured to trust insecure HTTP pulls from `registry.insecure_endpoints` (if set), otherwise from the host's management network gateway IP (e.g. `192.168.120.1:5000`)
3. **If `registry.containers` is set**, dpu-sim also builds/pushes those images and uses them in CNI deployment paths

#### Rebuilding and Redeploying CNI Images

After making changes to the CNI source code, you can rebuild and redeploy without tearing down the entire environment:

```bash
# Rebuild the CNI image and push to the local registry
$ ./bin/dpu-sim --rebuild-cni

# Rebuild AND rolling-restart CNI pods on all clusters
$ ./bin/dpu-sim --rebuild-cni --redeploy-cni
```

The `--rebuild-cni` flag requires `registry.enabled=true` and at least one `registry.containers` entry. It builds all configured container images and pushes them to the registry. Adding `--redeploy-cni` triggers a rolling restart of the CNI daemonsets so pods pick up the new image.

#### OVN-Kubernetes Source

The OVN-Kubernetes source code is included as a git submodule under `ovn-kubernetes/`. If the submodule is not initialized, dpu-sim will automatically initialize it during the build. To work on OVN-Kubernetes changes:

```bash
# Initialize the submodule (if not already done)
git submodule update --init ovn-kubernetes

# Make changes in ovn-kubernetes/
cd ovn-kubernetes
# ... edit code ...

# Rebuild and redeploy
cd ..
./bin/dpu-sim --rebuild-cni --redeploy-cni
```

### Using Different Cloud Image Versions

Update the `operating_system.image_url` in `config.yaml` to point to a different Cloud image:

For Fedora visit the downloads website https://download.fedoraproject.org/pub/fedora/linux/releases/ and pick the version that is required.

```yaml
operating_system:
  # Download from https://download.fedoraproject.org/pub/fedora/linux/releases/
  image_url: https://mirror.xenyth.net/fedora/linux/releases/43/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-43-1.6.x86_64.qcow2
  image_name: "Fedora-x86_64.qcow2"
```

## Usage

The `dpu-sim` executable automatically detects whether to use VM or Kind mode based on your config file.

### Deployment Options

```bash
# Auto-detect mode from default config (default: config.yaml = VM mode)
$ ./bin/dpu-sim

# Use Kind mode explicitly
$ ./bin/dpu-sim --config config-kind.yaml

# Skip cleanup (for incremental changes)
$ ./bin/dpu-sim --skip-cleanup

# Cleanup only
$ ./bin/dpu-sim --cleanup

# Review all avaialable options
$ ./bin/dpu-sim --help
DPU Simulator automates deployment of DPU simulation environments
using either VMs (libvirt) or containers (Kind), pre-configured with
Kubernetes and CNI for container networking experiments.

This is the main orchestrator that runs the complete deployment workflow:
  1. Install dependencies
  2. Clean up existing resources (Idempotent deployment - can be run multiple times safely)
  3. Deploy infrastructure (VMs or Kind clusters)
  4. Install Kubernetes and CNI components

Usage:
  dpu-sim [flags]

Flags:
      --cleanup            Only cleanup existing resources, do not deploy
      --config string      Path to configuration file (default "config.yaml")
  -h, --help               help for dpu-sim
      --log-level string   Log level (error, warn, info, debug) (default "info")
      --skip-cleanup       Skip cleanup of existing resources
      --skip-deploy        Skip VM/Kind deployment
      --skip-deps          Skip dependency checks
      --skip-k8s           Skip Kubernetes (VM only) and CNI installation

# After deployment, use the cluster
$ export KUBECONFIG=kubeconfig/cluster-1.kubeconfig
$ kubectl get nodes
$ kubectl get pods -A
```

### VM/Kind Mode Usage

#### Step 1: Ensuring dpu-sim is compiled

Binaries are located by default in `bin`. Make sure dpu-sim compiles sucessfully with the go compiler.

#### Step 2a: Deploy (VM)

Deploy all Host and DPU VMs and the network:

```bash
$ ./bin/dpu-sim
╔═══════════════════════════════════════════════╗
║               DPU Simulator                   ║
╚═══════════════════════════════════════════════╝
Configuration: config.yaml
Deployment mode: vm

=== Checking Dependencies ===
✓ Detected Linux distribution: rhel 9.6 (package manager: dnf, architecture: x86_64)
✓ wget is installed
✓ pip3 is installed
✓ jinjanator is installed
✓ git is installed
✓ openvswitch is installed
✓ libvirt is installed
✓ qemu-kvm is installed
✓ qemu-img is installed
✓ libvirt-devel is installed
✓ virt-install is installed
✓ genisoimage is installed
✓ aarch64-uefi-firmware is installed
✓ All dependencies are available

=== Cleaning up K8s ===

=== Setting up Local Container Registry ===
Starting local container registry...
Registry container dpu-sim-registry is already running
Using cached OVN-Kubernetes image ovn-kube:dpu-sim-a0aee420b71ed553
Tagging ovn-kube:dpu-sim -> localhost:5000/ovn-kube:dpu-sim
Pushing localhost:5000/ovn-kube:dpu-sim to local registry...
Getting image source signatures
Copying blob b6099ea2ca79 skipped: already exists
Copying blob a18bb338fdf0 skipped: already exists
Copying blob e045ad279873 skipped: already exists
Copying blob 9053dfc32224 skipped: already exists
Copying blob 4e85d09b008b skipped: already exists
Copying blob 700ace930faf skipped: already exists
Copying blob 7ff09adb94dc skipped: already exists
Copying blob 72794559b97f skipped: already exists
Copying blob 4193b4f92033 skipped: already exists
Copying blob ce8da18e86b5 skipped: already exists
Copying blob f64cb6185bfd skipped: already exists
Copying blob c68cde450fb6 skipped: already exists
Copying blob c2711023c9d9 skipped: already exists
Copying blob 31f89951ebb5 skipped: already exists
Copying blob 6b42ea3d3704 skipped: already exists
Copying blob 7edb4289d085 skipped: already exists
Copying blob 70d34a23c00a skipped: already exists
Copying blob 81a5ef580ffd skipped: already exists
Copying blob 494fc9abf297 skipped: already exists
Copying config 7618d98e77 done   |
Writing manifest to image destination
Pushed localhost:5000/ovn-kube:dpu-sim to local registry
Registry setup complete

╔═══════════════════════════════════════════════╗
║       VM-Based Deployment Workflow            ║
╚═══════════════════════════════════════════════╝
=== Cleaning up VMs ===
✓ Deleted disk: /var/lib/libvirt/images/master-1.qcow2
✓ Deleted cloud-init ISO: /var/lib/libvirt/images/master-1-cloud-init.iso
✓ UEFI NVRAM for master-1 does not exist, skipping deletion
✓ Cleaned up VM: master-1
✓ Deleted disk: /var/lib/libvirt/images/master-2.qcow2
✓ Deleted cloud-init ISO: /var/lib/libvirt/images/master-2-cloud-init.iso
✓ UEFI NVRAM for master-2 does not exist, skipping deletion
✓ Cleaned up VM: master-2
✓ Deleted disk: /var/lib/libvirt/images/host-1-1.qcow2
✓ Deleted cloud-init ISO: /var/lib/libvirt/images/host-1-1-cloud-init.iso
✓ UEFI NVRAM for host-1-1 does not exist, skipping deletion
✓ Cleaned up VM: host-1-1
✓ Deleted disk: /var/lib/libvirt/images/dpu-1-1.qcow2
✓ Deleted cloud-init ISO: /var/lib/libvirt/images/dpu-1-1-cloud-init.iso
✓ UEFI NVRAM for dpu-1-1 does not exist, skipping deletion
✓ Cleaned up VM: dpu-1-1
✓ Deleted disk: /var/lib/libvirt/images/host-2-1.qcow2
✓ Deleted cloud-init ISO: /var/lib/libvirt/images/host-2-1-cloud-init.iso
✓ UEFI NVRAM for host-2-1 does not exist, skipping deletion
✓ Cleaned up VM: host-2-1
✓ Deleted disk: /var/lib/libvirt/images/dpu-2-1.qcow2
✓ Deleted cloud-init ISO: /var/lib/libvirt/images/dpu-2-1-cloud-init.iso
✓ UEFI NVRAM for dpu-2-1 does not exist, skipping deletion
✓ Cleaned up VM: dpu-2-1
=== Cleaning up Networks ===
✓ Removed network mgmt-network
✓ Removed network ovn-network
✓ Removed network data-l2-network
✓ Removed network host-to-dpu-link
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-0 (bridge: h2d-7dd9fa8cc81)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-1 (bridge: h2d-72a38f6e39d)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-2 (bridge: h2d-e146469843c)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-3 (bridge: h2d-55c3625f3c5)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-4 (bridge: h2d-eaee54af833)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-5 (bridge: h2d-f435117044a)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-6 (bridge: h2d-7f9cef517ca)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-7 (bridge: h2d-7250ad47547)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-8 (bridge: h2d-e6421a32fa0)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-9 (bridge: h2d-9f889e5deea)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-10 (bridge: h2d-c74e760f1d7)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-11 (bridge: h2d-ee17a44b384)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-12 (bridge: h2d-115518888ad)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-13 (bridge: h2d-b0a8c670e1e)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-14 (bridge: h2d-60a21b9d47c)
✓ Removed host-to-DPU network h2d-host-1-1-dpu-1-1-15 (bridge: h2d-b229d56859f)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-0 (bridge: h2d-1adf8a8aaac)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-1 (bridge: h2d-b476efa8ac2)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-2 (bridge: h2d-5e4c942e9e6)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-3 (bridge: h2d-7d8702733f0)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-4 (bridge: h2d-d366284102d)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-5 (bridge: h2d-3fec93e2c83)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-6 (bridge: h2d-003b75ea8eb)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-7 (bridge: h2d-2896f1a79ab)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-8 (bridge: h2d-942f2023422)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-9 (bridge: h2d-e71b414d84b)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-10 (bridge: h2d-0e31d195178)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-11 (bridge: h2d-53b0f14fbd0)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-12 (bridge: h2d-61ee66b294b)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-13 (bridge: h2d-0fb7afedde8)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-14 (bridge: h2d-b2230805515)
✓ Removed host-to-DPU network h2d-host-2-1-dpu-2-1-15 (bridge: h2d-2abde8ad9d6)

=== Deploying VMs ===
=== Creating Networks ===
✓ Created network: mgmt-network
✓ Created network: ovn-network
✓ Created OVS bridge: ovs-data
✓ Created network: data-l2-network
✓ Created OVS bridge: h2d-1adf8a8aaac
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-0 (bridge: h2d-1adf8a8aaac)
✓ Created OVS bridge: h2d-b476efa8ac2
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-1 (bridge: h2d-b476efa8ac2)
✓ Created OVS bridge: h2d-5e4c942e9e6
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-2 (bridge: h2d-5e4c942e9e6)
✓ Created OVS bridge: h2d-7d8702733f0
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-3 (bridge: h2d-7d8702733f0)
✓ Created OVS bridge: h2d-d366284102d
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-4 (bridge: h2d-d366284102d)
✓ Created OVS bridge: h2d-3fec93e2c83
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-5 (bridge: h2d-3fec93e2c83)
✓ Created OVS bridge: h2d-003b75ea8eb
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-6 (bridge: h2d-003b75ea8eb)
✓ Created OVS bridge: h2d-2896f1a79ab
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-7 (bridge: h2d-2896f1a79ab)
✓ Created OVS bridge: h2d-942f2023422
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-8 (bridge: h2d-942f2023422)
✓ Created OVS bridge: h2d-e71b414d84b
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-9 (bridge: h2d-e71b414d84b)
✓ Created OVS bridge: h2d-0e31d195178
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-10 (bridge: h2d-0e31d195178)
✓ Created OVS bridge: h2d-53b0f14fbd0
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-11 (bridge: h2d-53b0f14fbd0)
✓ Created OVS bridge: h2d-61ee66b294b
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-12 (bridge: h2d-61ee66b294b)
✓ Created OVS bridge: h2d-0fb7afedde8
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-13 (bridge: h2d-0fb7afedde8)
✓ Created OVS bridge: h2d-b2230805515
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-14 (bridge: h2d-b2230805515)
✓ Created OVS bridge: h2d-2abde8ad9d6
✓ Created host-to-DPU network: h2d-host-2-1-dpu-2-1-15 (bridge: h2d-2abde8ad9d6)
✓ Created OVS bridge: h2d-7dd9fa8cc81
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-0 (bridge: h2d-7dd9fa8cc81)
✓ Created OVS bridge: h2d-72a38f6e39d
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-1 (bridge: h2d-72a38f6e39d)
✓ Created OVS bridge: h2d-e146469843c
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-2 (bridge: h2d-e146469843c)
✓ Created OVS bridge: h2d-55c3625f3c5
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-3 (bridge: h2d-55c3625f3c5)
✓ Created OVS bridge: h2d-eaee54af833
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-4 (bridge: h2d-eaee54af833)
✓ Created OVS bridge: h2d-f435117044a
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-5 (bridge: h2d-f435117044a)
✓ Created OVS bridge: h2d-7f9cef517ca
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-6 (bridge: h2d-7f9cef517ca)
✓ Created OVS bridge: h2d-7250ad47547
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-7 (bridge: h2d-7250ad47547)
✓ Created OVS bridge: h2d-e6421a32fa0
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-8 (bridge: h2d-e6421a32fa0)
✓ Created OVS bridge: h2d-9f889e5deea
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-9 (bridge: h2d-9f889e5deea)
✓ Created OVS bridge: h2d-c74e760f1d7
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-10 (bridge: h2d-c74e760f1d7)
✓ Created OVS bridge: h2d-ee17a44b384
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-11 (bridge: h2d-ee17a44b384)
✓ Created OVS bridge: h2d-115518888ad
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-12 (bridge: h2d-115518888ad)
✓ Created OVS bridge: h2d-b0a8c670e1e
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-13 (bridge: h2d-b0a8c670e1e)
✓ Created OVS bridge: h2d-60a21b9d47c
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-14 (bridge: h2d-60a21b9d47c)
✓ Created OVS bridge: h2d-b229d56859f
✓ Created host-to-DPU network: h2d-host-1-1-dpu-1-1-15 (bridge: h2d-b229d56859f)
✓ All networks created successfully
=== Creating All VMs ===
=== Creating VM: master-1 ===
✓ Image already exists at /var/lib/libvirt/images/Fedora-x86_64.qcow2, skipping download
✓ Created disk for master-1: /var/lib/libvirt/images/master-1.qcow2
✓ Created cloud-init ISO: /var/lib/libvirt/images/master-1-cloud-init.iso
✓ Created and started VM: master-1
=== Creating VM: master-2 ===
✓ Image already exists at /var/lib/libvirt/images/Fedora-x86_64.qcow2, skipping download
✓ Created disk for master-2: /var/lib/libvirt/images/master-2.qcow2
✓ Created cloud-init ISO: /var/lib/libvirt/images/master-2-cloud-init.iso
✓ Created and started VM: master-2
=== Creating VM: host-1-1 ===
✓ Image already exists at /var/lib/libvirt/images/Fedora-x86_64.qcow2, skipping download
✓ Created disk for host-1-1: /var/lib/libvirt/images/host-1-1.qcow2
✓ Created cloud-init ISO: /var/lib/libvirt/images/host-1-1-cloud-init.iso
✓ Created and started VM: host-1-1
=== Creating VM: dpu-1-1 ===
✓ Image already exists at /var/lib/libvirt/images/Fedora-x86_64.qcow2, skipping download
✓ Created disk for dpu-1-1: /var/lib/libvirt/images/dpu-1-1.qcow2
✓ Created cloud-init ISO: /var/lib/libvirt/images/dpu-1-1-cloud-init.iso
✓ Created and started VM: dpu-1-1
=== Creating VM: host-2-1 ===
✓ Image already exists at /var/lib/libvirt/images/Fedora-x86_64.qcow2, skipping download
✓ Created disk for host-2-1: /var/lib/libvirt/images/host-2-1.qcow2
✓ Created cloud-init ISO: /var/lib/libvirt/images/host-2-1-cloud-init.iso
✓ Created and started VM: host-2-1
=== Creating VM: dpu-2-1 ===
✓ Image already exists at /var/lib/libvirt/images/Fedora-x86_64.qcow2, skipping download
✓ Created disk for dpu-2-1: /var/lib/libvirt/images/dpu-2-1.qcow2
✓ Created cloud-init ISO: /var/lib/libvirt/images/dpu-2-1-cloud-init.iso
✓ Created and started VM: dpu-2-1
✓ All VMs created successfully

=== Waiting for VMs to boot and get IPs ===
Waiting for master-1 to get an IP address...
✓ master-1 IP: 192.168.120.51
Waiting for SSH on master-1...
✓ SSH ready on master-1
Waiting for master-2 to get an IP address...
✓ master-2 IP: 192.168.120.24
Waiting for SSH on master-2...
✓ SSH ready on master-2
Waiting for host-1-1 to get an IP address...
✓ host-1-1 IP: 192.168.120.25
Waiting for SSH on host-1-1...
✓ SSH ready on host-1-1
Waiting for dpu-1-1 to get an IP address...
✓ dpu-1-1 IP: 192.168.120.94
Waiting for SSH on dpu-1-1...
✓ SSH ready on dpu-1-1
Waiting for host-2-1 to get an IP address...
✓ host-2-1 IP: 192.168.120.69
Waiting for SSH on host-2-1...
✓ SSH ready on host-2-1
Waiting for dpu-2-1 to get an IP address...
✓ dpu-2-1 IP: 192.168.120.85
Waiting for SSH on dpu-2-1...
✓ SSH ready on dpu-2-1

=== Installing Kubernetes and CNI ===
=== Installing Kubernetes on VM-based deployment ===
--- Installing Kubernetes on master-1 (192.168.120.51) ---
Installing Kubernetes on master-1 (ssh://root@192.168.120.51)...
✓ Hostname set to master-1
✓ Detected Linux distribution: fedora 43 (package manager: dnf, architecture: x86_64)
✓ Disable firewalld is installed
Installing missing dependencies: Swap Off, K8s Kernel Modules, crio, openvswitch, NetworkManager-ovs, Kubelet Tools
Installing Swap Off for fedora on ssh://root@192.168.120.51...
✓ Swap Off installed
Installing K8s Kernel Modules for fedora on ssh://root@192.168.120.51...
✓ K8s Kernel Modules installed
Installing crio for fedora on ssh://root@192.168.120.51...
✓ crio installed
Installing openvswitch for fedora on ssh://root@192.168.120.51...
✓ openvswitch installed
Installing NetworkManager-ovs for fedora on ssh://root@192.168.120.51...
✓ NetworkManager-ovs installed
Installing Kubelet Tools for fedora on ssh://root@192.168.120.51...
✓ Kubelet Tools installed
✓ All dependencies are available
✓ Kubernetes 1.33 installed on master-1
--- Installing Kubernetes on master-2 (192.168.120.24) ---
Installing Kubernetes on master-2 (ssh://root@192.168.120.24)...
✓ Hostname set to master-2
✓ Detected Linux distribution: fedora 43 (package manager: dnf, architecture: x86_64)
✓ Disable firewalld is installed
Installing missing dependencies: Swap Off, K8s Kernel Modules, crio, openvswitch, NetworkManager-ovs, Kubelet Tools
Installing Swap Off for fedora on ssh://root@192.168.120.24...
✓ Swap Off installed
Installing K8s Kernel Modules for fedora on ssh://root@192.168.120.24...
✓ K8s Kernel Modules installed
Installing crio for fedora on ssh://root@192.168.120.24...
✓ crio installed
Installing openvswitch for fedora on ssh://root@192.168.120.24...
✓ openvswitch installed
Installing NetworkManager-ovs for fedora on ssh://root@192.168.120.24...
✓ NetworkManager-ovs installed
Installing Kubelet Tools for fedora on ssh://root@192.168.120.24...
✓ Kubelet Tools installed
✓ All dependencies are available
✓ Kubernetes 1.33 installed on master-2
--- Installing Kubernetes on host-1-1 (192.168.120.25) ---
Installing Kubernetes on host-1-1 (ssh://root@192.168.120.25)...
✓ Hostname set to host-1-1
✓ Detected Linux distribution: fedora 43 (package manager: dnf, architecture: x86_64)
✓ Disable firewalld is installed
Installing missing dependencies: Swap Off, K8s Kernel Modules, crio, openvswitch, NetworkManager-ovs, Kubelet Tools
Installing Swap Off for fedora on ssh://root@192.168.120.25...
✓ Swap Off installed
Installing K8s Kernel Modules for fedora on ssh://root@192.168.120.25...
✓ K8s Kernel Modules installed
Installing crio for fedora on ssh://root@192.168.120.25...
✓ crio installed
Installing openvswitch for fedora on ssh://root@192.168.120.25...
✓ openvswitch installed
Installing NetworkManager-ovs for fedora on ssh://root@192.168.120.25...
✓ NetworkManager-ovs installed
Installing Kubelet Tools for fedora on ssh://root@192.168.120.25...
✓ Kubelet Tools installed
✓ All dependencies are available
✓ Kubernetes 1.33 installed on host-1-1
--- Installing Kubernetes on dpu-1-1 (192.168.120.94) ---
Installing Kubernetes on dpu-1-1 (ssh://root@192.168.120.94)...
✓ Hostname set to dpu-1-1
✓ Detected Linux distribution: fedora 43 (package manager: dnf, architecture: x86_64)
✓ Disable firewalld is installed
Installing missing dependencies: Swap Off, K8s Kernel Modules, crio, openvswitch, NetworkManager-ovs, Kubelet Tools
Installing Swap Off for fedora on ssh://root@192.168.120.94...
✓ Swap Off installed
Installing K8s Kernel Modules for fedora on ssh://root@192.168.120.94...
✓ K8s Kernel Modules installed
Installing crio for fedora on ssh://root@192.168.120.94...
✓ crio installed
Installing openvswitch for fedora on ssh://root@192.168.120.94...
✓ openvswitch installed
Installing NetworkManager-ovs for fedora on ssh://root@192.168.120.94...
✓ NetworkManager-ovs installed
Installing Kubelet Tools for fedora on ssh://root@192.168.120.94...
✓ Kubelet Tools installed
✓ All dependencies are available
✓ Kubernetes 1.33 installed on dpu-1-1
--- Installing Kubernetes on host-2-1 (192.168.120.69) ---
Installing Kubernetes on host-2-1 (ssh://root@192.168.120.69)...
✓ Hostname set to host-2-1
✓ Detected Linux distribution: fedora 43 (package manager: dnf, architecture: x86_64)
✓ Disable firewalld is installed
Installing missing dependencies: Swap Off, K8s Kernel Modules, crio, openvswitch, NetworkManager-ovs, Kubelet Tools
Installing Swap Off for fedora on ssh://root@192.168.120.69...
✓ Swap Off installed
Installing K8s Kernel Modules for fedora on ssh://root@192.168.120.69...
✓ K8s Kernel Modules installed
Installing crio for fedora on ssh://root@192.168.120.69...
✓ crio installed
Installing openvswitch for fedora on ssh://root@192.168.120.69...
✓ openvswitch installed
Installing NetworkManager-ovs for fedora on ssh://root@192.168.120.69...
✓ NetworkManager-ovs installed
Installing Kubelet Tools for fedora on ssh://root@192.168.120.69...
✓ Kubelet Tools installed
✓ All dependencies are available
✓ Kubernetes 1.33 installed on host-2-1
--- Installing Kubernetes on dpu-2-1 (192.168.120.85) ---
Installing Kubernetes on dpu-2-1 (ssh://root@192.168.120.85)...
✓ Hostname set to dpu-2-1
✓ Detected Linux distribution: fedora 43 (package manager: dnf, architecture: x86_64)
✓ Disable firewalld is installed
Installing missing dependencies: Swap Off, K8s Kernel Modules, crio, openvswitch, NetworkManager-ovs, Kubelet Tools
Installing Swap Off for fedora on ssh://root@192.168.120.85...
✓ Swap Off installed
Installing K8s Kernel Modules for fedora on ssh://root@192.168.120.85...
✓ K8s Kernel Modules installed
Installing crio for fedora on ssh://root@192.168.120.85...
✓ crio installed
Installing openvswitch for fedora on ssh://root@192.168.120.85...
✓ openvswitch installed
Installing NetworkManager-ovs for fedora on ssh://root@192.168.120.85...
✓ NetworkManager-ovs installed
Installing Kubelet Tools for fedora on ssh://root@192.168.120.85...
✓ Kubelet Tools installed
✓ All dependencies are available
✓ Kubernetes 1.33 installed on dpu-2-1

=== Setting up Kubernetes cluster cluster-1 ===

=== Initializing first control plane node: master-1 ===
Initializing control plane on master-1 (ssh://root@192.168.120.51)...
K8s IP: 192.168.123.11 Pod CIDR: 10.244.0.0/16, Service CIDR: 10.245.0.0/16
Setting up kubectl on master-1 (ssh://root@192.168.120.51)...
✓ Control plane initialized on master-1
Worker join command: kubeadm join 192.168.123.11:6443 --token tynsk1.y7yqc67h3nohn38p --discovery-token-ca-cert-hash sha256:2392da0edf79d8cc06c666016800a8923c808bdf7ab2740e910cdf99701bb264
Control plane join command: kubeadm join 192.168.123.11:6443 --token tynsk1.y7yqc67h3nohn38p --discovery-token-ca-cert-hash sha256:2392da0edf79d8cc06c666016800a8923c808bdf7ab2740e910cdf99701bb264 --control-plane --certificate-key c8dbf7978fb92dc593f10e2ac125443d925e134cc58565fbbbee77ffdac46a0a
API server endpoint: https://192.168.123.11:6443
✓ Kubeconfig saved to: kubeconfig/cluster-1.kubeconfig

=== Installing ovn-kubernetes CNI on cluster cluster-1 ===
Installing OVN-Kubernetes via Helm: Pod CIDR: 10.244.0.0/16, Service CIDR: 10.245.0.0/16, API Server: https://192.168.123.11:6443
Patching CoreDNS configmap for OVN-Kubernetes compatibility, dns server: 8.8.8.8
✓ CoreDNS configmap patched successfully
Using cached OVN-Kubernetes image ovn-kube-fedora:dpu-sim-a0aee420b71ed553
Using local registry image for OVN-Kubernetes Helm deployment: 192.168.120.1:5000/ovn-kube:dpu-sim
Labeling nodes for single-node-zone interconnect...
✓ All nodes labeled for single-node-zone interconnect
✓ Master nodes labeled for OVN-Kubernetes HA
Running helm install for OVN-Kubernetes (chart: /root/dpu-sim/ovn-kubernetes/helm/ovn-kubernetes, values: values-single-node-zone.yaml)...
✓ Helm install completed successfully
Applying external CRD manifests (ANP/BANP)...
✓ External CRDs applied successfully
Waiting for all pods in namespace: ovn-kubernetes to be ready...
✓ All Pods in namespace: ovn-kubernetes are ready
✓ OVN-Kubernetes pods are ready, installed via Helm successfully!
✓ Deleted DaemonSet kube-system/kube-proxy
=== Joining worker nodes ===
✓ Worker node joined to Kubernetes cluster: host-1-1
✓ Worker node joined to Kubernetes cluster: host-2-1
✓ Kubernetes cluster cluster-1 setup complete

=== Setting up Kubernetes cluster cluster-2 ===

=== Initializing first control plane node: master-2 ===
Initializing control plane on master-2 (ssh://root@192.168.120.24)...
K8s IP: 192.168.123.21 Pod CIDR: 10.246.0.0/16, Service CIDR: 10.247.0.0/16
Setting up kubectl on master-2 (ssh://root@192.168.120.24)...
✓ Control plane initialized on master-2
Worker join command: kubeadm join 192.168.123.21:6443 --token 8767y8.zzkx2knvdjdtp6ju --discovery-token-ca-cert-hash sha256:970fd73cdf8aec7c1200fb11c7633a74c5742bf6dea9cea1b58949dd62e6307e
Control plane join command: kubeadm join 192.168.123.21:6443 --token 8767y8.zzkx2knvdjdtp6ju --discovery-token-ca-cert-hash sha256:970fd73cdf8aec7c1200fb11c7633a74c5742bf6dea9cea1b58949dd62e6307e --control-plane --certificate-key 5d39589144310897db083c3fe95d12d6c44746cfa578398325ad407811004e2b
API server endpoint: https://192.168.123.21:6443
✓ Kubeconfig saved to: kubeconfig/cluster-2.kubeconfig

=== Installing ovn-kubernetes CNI on cluster cluster-2 ===
Installing OVN-Kubernetes via Helm: Pod CIDR: 10.246.0.0/16, Service CIDR: 10.247.0.0/16, API Server: https://192.168.123.21:6443
Patching CoreDNS configmap for OVN-Kubernetes compatibility, dns server: 8.8.8.8
✓ CoreDNS configmap patched successfully
Using cached OVN-Kubernetes image ovn-kube-fedora:dpu-sim-a0aee420b71ed553
Using local registry image for OVN-Kubernetes Helm deployment: 192.168.120.1:5000/ovn-kube:dpu-sim
Labeling nodes for single-node-zone interconnect...
✓ All nodes labeled for single-node-zone interconnect
✓ Master nodes labeled for OVN-Kubernetes HA
Running helm install for OVN-Kubernetes (chart: /root/dpu-sim/ovn-kubernetes/helm/ovn-kubernetes, values: values-single-node-zone.yaml)...
✓ Helm install completed successfully
Applying external CRD manifests (ANP/BANP)...
✓ External CRDs applied successfully
Waiting for all pods in namespace: ovn-kubernetes to be ready...
✓ All Pods in namespace: ovn-kubernetes are ready
✓ OVN-Kubernetes pods are ready, installed via Helm successfully!
✓ Deleted DaemonSet kube-system/kube-proxy
=== Joining worker nodes ===
✓ Worker node joined to Kubernetes cluster: dpu-1-1
✓ Worker node joined to Kubernetes cluster: dpu-2-1
✓ Kubernetes cluster cluster-2 setup complete

╔═══════════════════════════════════════════════╗
║         Deployment Completed Successfully!    ║
╚═══════════════════════════════════════════════╝

✓ VM deployment complete!

Your DPU simulation environment is ready:
  • VMs are running and accessible
  • Kubernetes is installed and configured
  • CNI is deployed and ready

Useful commands:
  vmctl list                    # List all VMs
  vmctl ssh <vm-name>           # SSH into a VM
  kubectl --kubeconfig kubeconfig/cluster-1.kubeconfig get nodes
  kubectl --kubeconfig kubeconfig/cluster-2.kubeconfig get nodes

Kubeconfig directory: kubeconfig
For more information, see README.md
```

This will:
1. **Clean up any existing VMs and networks** (idempotent deployment - can be run multiple times safely)
2. Download Cloud Base image (if not present) - We recommend to download from Fedora.
3. Create custom libvirt networks. All Host and DPUs will have a dedicated connection between them to simulate a DPU's general design.
4. Create and start all Host and DPU VMs with cloud-init configuration
5. Wait for VMs to boot and get IP addresses
6. Kubernetes gets installed on all VMs.
  a. Disable swap (required for Kubernetes)
  b. Configure kernel modules (overlay, br_netfilter)
  c. Install and configure CRI-O
  d. Install Open vSwitch
  e. Installs Kubernetes components (`kubeadm`, `kubelet`, `kubectl`)
  f. Disable the firewall
7. One master is chosen to bootstrap the cluster with `kubeadm`
  a. Additional masters also join the cluster
8. All workers join the cluster with `kubeadm`
7. CNI gets installed on the cluster.
9. Workload pods can now be deployed on the cluster once `dpu-sim` runs to completion sucessfully.

**Note:** The `dpu-sim` application is idempotent by default - it automatically cleans up existing resources before deploying. You can run it multiple times safely. If you want to skip cleanup for some reason, use `dpu-sim --skip-cleanup`

#### Step 2b: Deploy (Kind)

Deploy all Host and DPU Containers and the network:

```bash
$ ./bin/dpu-sim --config=config-kind.yaml
╔═══════════════════════════════════════════════╗
║               DPU Simulator                   ║
╚═══════════════════════════════════════════════╝
Configuration: config-kind.yaml
Deployment mode: kind

=== Checking Dependencies ===
✓ Detected Linux distribution: rhel 9.6 (package manager: dnf, architecture: x86_64)
✓ wget is installed
✓ pip3 is installed
✓ jinjanator is installed
✓ git is installed
✓ openvswitch is installed
✓ kubectl is installed
✓ Container Runtime is installed
✓ kind is installed
✓ All dependencies are available

=== Cleaning up K8s ===
✓ Kubeconfig file removed: kubeconfig/dpu-sim-dpu-kind.kubeconfig
✓ Kubeconfig file removed: kubeconfig/dpu-sim-host-kind.kubeconfig

=== Setting up Local Container Registry ===
Starting local container registry...
Registry container dpu-sim-registry is already running
Using cached OVN-Kubernetes image ovn-kube:dpu-sim-d56f4ef8c6196df5
Tagging ovn-kube:dpu-sim -> localhost:5000/ovn-kube:dpu-sim
Pushing localhost:5000/ovn-kube:dpu-sim to local registry...
Getting image source signatures
Copying blob bd9ddc54bea9 skipped: already exists
Copying blob 4e85d09b008b skipped: already exists
Copying blob f4401750440f skipped: already exists
Copying blob 4b6ea26202b1 skipped: already exists
Copying blob f3fb97b9254b skipped: already exists
Copying blob b6099ea2ca79 skipped: already exists
Copying blob b3b92927a3d1 skipped: already exists
Copying blob 5c2d07f0c5e8 skipped: already exists
Copying blob c3ad7b174a88 skipped: already exists
Copying blob 7dc0f2d3331e skipped: already exists
Copying blob 6fd44274f445 skipped: already exists
Copying blob 58ca6ce0a39b skipped: already exists
Copying blob a2d0252ed857 skipped: already exists
Copying blob cc577348e02b skipped: already exists
Copying blob 452dbfba6698 skipped: already exists
Copying blob 2a28b27abe91 skipped: already exists
Copying blob 64b4ff24ee9d skipped: already exists
Copying blob d144dfb53d21 skipped: already exists
Copying blob 6bef6fc09ddc skipped: already exists
Copying config 022d0a0f6b done   |
Writing manifest to image destination
Pushed localhost:5000/ovn-kube:dpu-sim to local registry
Registry setup complete

╔═══════════════════════════════════════════════╗
║      Kind-Based Deployment Workflow           ║
╚═══════════════════════════════════════════════╝

=== Cleaning up existing kind clusters ===
Deleting Kind cluster: dpu-sim-host-kind
✓ Deleted Kind cluster: dpu-sim-host-kind
Deleting Kind cluster: dpu-sim-dpu-kind
✓ Deleted Kind cluster: dpu-sim-dpu-kind

=== Ensuring Kind host prerequisites ===
✓ Detected Linux distribution: rhel 9.6 (package manager: dnf, architecture: x86_64)
✓ Inotify Limits is installed
✓ All dependencies are available

=== Deploying Kind clusters ===

=== Creating Kind Clusters ===
Creating Kind cluster: dpu-sim-host-kind
✓ Created Kind cluster: dpu-sim-host-kind
✓ Kubeconfig saved to: kubeconfig/dpu-sim-host-kind.kubeconfig
Creating Kind cluster: dpu-sim-dpu-kind
✓ Created Kind cluster: dpu-sim-dpu-kind
✓ Kubeconfig saved to: kubeconfig/dpu-sim-dpu-kind.kubeconfig
Registry IP on kind network: 10.89.0.125

Cluster: dpu-sim-host-kind
  Status: running
  Nodes:
    - dpu-sim-host-kind-control-plane (control-plane) [Unknown]
    - dpu-sim-host-kind-worker2 (worker) [Unknown]
    - dpu-sim-host-kind-worker (worker) [Unknown]
✓ Detected Linux distribution: debian 12 (package manager: apt, architecture: x86_64)
Installing missing dependencies: IPv6
Installing IPv6 for debian on docker://dpu-sim-host-kind-control-plane...
✓ IPv6 installed
✓ All dependencies are available
✓ Detected Linux distribution: debian 12 (package manager: apt, architecture: x86_64)
Installing missing dependencies: IPv6
Installing IPv6 for debian on docker://dpu-sim-host-kind-worker2...
✓ IPv6 installed
✓ All dependencies are available
✓ Detected Linux distribution: debian 12 (package manager: apt, architecture: x86_64)
Installing missing dependencies: IPv6
Installing IPv6 for debian on docker://dpu-sim-host-kind-worker...
✓ IPv6 installed
✓ All dependencies are available

Cluster: dpu-sim-dpu-kind
  Status: running
  Nodes:
    - dpu-sim-dpu-kind-control-plane (control-plane) [NotReady]
    - dpu-sim-dpu-kind-worker2 (worker) [NotReady]
    - dpu-sim-dpu-kind-worker (worker) [NotReady]
✓ Detected Linux distribution: debian 12 (package manager: apt, architecture: x86_64)
Installing missing dependencies: IPv6
Installing IPv6 for debian on docker://dpu-sim-dpu-kind-control-plane...
✓ IPv6 installed
✓ All dependencies are available
✓ Detected Linux distribution: debian 12 (package manager: apt, architecture: x86_64)
Installing missing dependencies: IPv6
Installing IPv6 for debian on docker://dpu-sim-dpu-kind-worker2...
✓ IPv6 installed
✓ All dependencies are available
✓ Detected Linux distribution: debian 12 (package manager: apt, architecture: x86_64)
Installing missing dependencies: IPv6
Installing IPv6 for debian on docker://dpu-sim-dpu-kind-worker...
✓ IPv6 installed
✓ All dependencies are available
Setting up veth topology for pair 0: dpu-sim-host-kind-worker <-> dpu-sim-dpu-kind-worker (16 data channels)
Setting up veth topology for pair 1: dpu-sim-host-kind-worker2 <-> dpu-sim-dpu-kind-worker2 (16 data channels)
✓ Veth topology created for 2 host-DPU pairs (16 data channels each)

=== Installing CNI ===

=== Installing CNI on Kind clusters ===

--- Installing CNI on cluster dpu-sim-host-kind ---
Using local registry image for OVN-Kubernetes (tag: ovn-kube:dpu-sim)
Internal API server IP for cluster dpu-sim-host-kind: 10.89.0.6

=== Installing ovn-kubernetes CNI on cluster dpu-sim-host-kind ===
Installing OVN-Kubernetes via Helm: Pod CIDR: 10.244.0.0/16, Service CIDR: 10.245.0.0/16, API Server: https://10.89.0.6:6443
Patching CoreDNS configmap for OVN-Kubernetes compatibility, dns server: 8.8.8.8
✓ CoreDNS configmap patched successfully
Using cached OVN-Kubernetes image ovn-kube-fedora:dpu-sim-d56f4ef8c6196df5
Using local registry image for OVN-Kubernetes Helm deployment: localhost:5000/ovn-kube:dpu-sim
Labeling nodes for single-node-zone interconnect...
✓ All nodes labeled for single-node-zone interconnect
✓ Master nodes labeled for OVN-Kubernetes HA
Running helm install for OVN-Kubernetes (chart: /root/dpu-sim/ovn-kubernetes/helm/ovn-kubernetes, values: values-single-node-zone.yaml)...
✓ Helm install completed successfully
Applying external CRD manifests (ANP/BANP)...
✓ External CRDs applied successfully
Waiting for all pods in namespace: ovn-kubernetes to be ready...
✓ All Pods in namespace: ovn-kubernetes are ready
✓ OVN-Kubernetes pods are ready, installed via Helm successfully!
DaemonSet kube-system/kube-proxy does not exist, skipping deletion

--- Installing CNI on cluster dpu-sim-dpu-kind ---
Using local registry image for OVN-Kubernetes (tag: ovn-kube:dpu-sim)
Internal API server IP for cluster dpu-sim-dpu-kind: 10.89.0.11

=== Installing ovn-kubernetes CNI on cluster dpu-sim-dpu-kind ===
Installing OVN-Kubernetes via Helm: Pod CIDR: 10.246.0.0/16, Service CIDR: 10.247.0.0/16, API Server: https://10.89.0.11:6443
Patching CoreDNS configmap for OVN-Kubernetes compatibility, dns server: 8.8.8.8
✓ CoreDNS configmap patched successfully
Using cached OVN-Kubernetes image ovn-kube-fedora:dpu-sim-d56f4ef8c6196df5
Using local registry image for OVN-Kubernetes Helm deployment: localhost:5000/ovn-kube:dpu-sim
Labeling nodes for single-node-zone interconnect...
✓ All nodes labeled for single-node-zone interconnect
✓ Master nodes labeled for OVN-Kubernetes HA
Running helm install for OVN-Kubernetes (chart: /root/dpu-sim/ovn-kubernetes/helm/ovn-kubernetes, values: values-single-node-zone.yaml)...
✓ Helm install completed successfully
Applying external CRD manifests (ANP/BANP)...
✓ External CRDs applied successfully
Waiting for all pods in namespace: ovn-kubernetes to be ready...
✓ All Pods in namespace: ovn-kubernetes are ready
✓ OVN-Kubernetes pods are ready, installed via Helm successfully!
DaemonSet kube-system/kube-proxy does not exist, skipping deletion

✓ CNI installation complete on Kind clusters

╔═══════════════════════════════════════════════╗
║         Deployment Completed Successfully!    ║
╚═══════════════════════════════════════════════╝

✓ Kind deployment complete!

Your DPU simulation environment is ready:
  • Kind clusters are running
  • CNI is deployed and ready

Useful commands:
  kind get clusters             # List all clusters
  kubectl --kubeconfig kubeconfig/dpu-sim-host-kind.kubeconfig get nodes
  kubectl --kubeconfig kubeconfig/dpu-sim-dpu-kind.kubeconfig get nodes

Kubeconfig directory: kubeconfig
For more information, see README.md
```


#### Key Features

- **Automatic Cluster Initialization**: No manual `kubeadm` commands needed
- **Multiple Cluster Support**: Deploy multiple independent K8s clusters in one configuration
- **Custom Pod Network CIDRs**: Each cluster can have its own pod and/or service network CIDR
- **Automatic CNI Installation**: Flannel or OVN-Kubernetes is automatically installed and configured
- **Role-Based Assignment**: VMs/Kind containers are assigned as `master` or `worker` nodes
- **Network Isolation**: Different clusters use different overlay networks

After Installation finished, you should expect these software packages to be running:
- CRI-O container runtime
- `kubelet` (Kubernetes node agent)
- Flannel and other containers are running, for example:
```bash
$ kubectl get pods -A -o wide
NAMESPACE      NAME                               READY   STATUS    RESTARTS   AGE   IP               NODE       NOMINATED NODE   READINESS GATES
kube-flannel   kube-flannel-ds-btnhv              1/1     Running   0          11m   192.168.100.86   dpu-1      <none>           <none>
kube-flannel   kube-flannel-ds-t7d44              1/1     Running   0          11m   192.168.100.14   master-1   <none>           <none>
kube-flannel   kube-flannel-ds-vdhjz              1/1     Running   0          11m   192.168.100.23   host-1     <none>           <none>
kube-system    coredns-674b8bbfcf-2g6tz           1/1     Running   0          11m   10.85.0.3        master-1   <none>           <none>
kube-system    coredns-674b8bbfcf-qhsw7           1/1     Running   0          11m   10.85.0.2        master-1   <none>           <none>
kube-system    etcd-master-1                      1/1     Running   0          11m   192.168.100.14   master-1   <none>           <none>
kube-system    kube-apiserver-master-1            1/1     Running   0          11m   192.168.100.14   master-1   <none>           <none>
kube-system    kube-controller-manager-master-1   1/1     Running   0          11m   192.168.100.14   master-1   <none>           <none>
kube-system    kube-multus-ds-jh2l5               1/1     Running   0          11m   192.168.100.86   dpu-1      <none>           <none>
kube-system    kube-multus-ds-rzqj2               1/1     Running   0          11m   192.168.100.23   host-1     <none>           <none>
kube-system    kube-multus-ds-vn4bv               1/1     Running   0          11m   192.168.100.14   master-1   <none>           <none>
kube-system    kube-proxy-69q6s                   1/1     Running   0          11m   192.168.100.23   host-1     <none>           <none>
kube-system    kube-proxy-9fq5x                   1/1     Running   0          11m   192.168.100.86   dpu-1      <none>           <none>
kube-system    kube-proxy-kc9fd                   1/1     Running   0          11m   192.168.100.14   master-1   <none>           <none>
kube-system    kube-scheduler-master-1            1/1     Running   0          11m   192.168.100.14   master-1   <none>           <none>
```

- OVN-Kubernetes and other containers are running, for example:
```bash
$ export KUBECONFIG=./kubeconfig/cluster-1.kubeconfig
$ oc get nodes
NAME       STATUS   ROLES           AGE   VERSION
host-1-1   Ready    <none>          30m   v1.33.9
host-2-1   Ready    <none>          30m   v1.33.9
master-1   Ready    control-plane   31m   v1.33.9
$ oc get pods -A -o wide
NAMESPACE        NAME                                     READY   STATUS    RESTARTS   AGE   IP               NODE       NOMINATED NODE   READINESS GATES
kube-system      coredns-674b8bbfcf-7mqxb                 1/1     Running   0          31m   10.85.0.3        master-1   <none>           <none>
kube-system      coredns-674b8bbfcf-plbkl                 1/1     Running   0          31m   10.85.0.2        master-1   <none>           <none>
kube-system      etcd-master-1                            1/1     Running   0          31m   192.168.120.51   master-1   <none>           <none>
kube-system      kube-apiserver-master-1                  1/1     Running   0          31m   192.168.120.51   master-1   <none>           <none>
kube-system      kube-controller-manager-master-1         1/1     Running   0          31m   192.168.120.51   master-1   <none>           <none>
kube-system      kube-scheduler-master-1                  1/1     Running   0          31m   192.168.120.51   master-1   <none>           <none>
ovn-kubernetes   ovnkube-control-plane-669fb74fd5-cqbnk   1/1     Running   0          31m   192.168.120.51   master-1   <none>           <none>
ovn-kubernetes   ovnkube-identity-rfhvn                   1/1     Running   0          31m   192.168.120.51   master-1   <none>           <none>
ovn-kubernetes   ovnkube-node-2m2h8                       6/6     Running   0          30m   192.168.120.69   host-2-1   <none>           <none>
ovn-kubernetes   ovnkube-node-65qps                       6/6     Running   0          30m   192.168.120.25   host-1-1   <none>           <none>
ovn-kubernetes   ovnkube-node-xwbrm                       6/6     Running   0          31m   192.168.120.51   master-1   <none>           <none>
$ export KUBECONFIG=./kubeconfig/cluster-2.kubeconfig
$ oc get nodes
NAME       STATUS   ROLES           AGE   VERSION
dpu-1-1    Ready    <none>          28m   v1.33.9
dpu-2-1    Ready    <none>          28m   v1.33.9
master-2   Ready    control-plane   30m   v1.33.9
$ oc get pods -A -o wide
NAMESPACE        NAME                                     READY   STATUS    RESTARTS   AGE   IP               NODE       NOMINATED NODE   READINESS GATES
kube-system      coredns-674b8bbfcf-5s592                 1/1     Running   0          30m   10.85.0.3        master-2   <none>           <none>
kube-system      coredns-674b8bbfcf-sctlw                 1/1     Running   0          30m   10.85.0.2        master-2   <none>           <none>
kube-system      etcd-master-2                            1/1     Running   0          30m   192.168.120.24   master-2   <none>           <none>
kube-system      kube-apiserver-master-2                  1/1     Running   0          30m   192.168.120.24   master-2   <none>           <none>
kube-system      kube-controller-manager-master-2         1/1     Running   0          30m   192.168.120.24   master-2   <none>           <none>
kube-system      kube-scheduler-master-2                  1/1     Running   0          30m   192.168.120.24   master-2   <none>           <none>
ovn-kubernetes   ovnkube-control-plane-669fb74fd5-l6c2c   1/1     Running   0          30m   192.168.120.24   master-2   <none>           <none>
ovn-kubernetes   ovnkube-identity-894gh                   1/1     Running   0          30m   192.168.120.24   master-2   <none>           <none>
ovn-kubernetes   ovnkube-node-7vvs2                       6/6     Running   0          28m   192.168.120.85   dpu-2-1    <none>           <none>
ovn-kubernetes   ovnkube-node-ph2zc                       6/6     Running   0          30m   192.168.120.24   master-2   <none>           <none>
ovn-kubernetes   ovnkube-node-xcghq                       6/6     Running   0          28m   192.168.120.94   dpu-1-1    <none>           <none>

```

- On kind with OVN-Kubernetes, it looks like this:
```bash
$ export KUBECONFIG=./kubeconfig/dpu-sim-host-kind.kubeconfig
$ oc get nodes
NAME                              STATUS   ROLES           AGE   VERSION
dpu-sim-host-kind-control-plane   Ready    control-plane   39m   v1.35.0
dpu-sim-host-kind-worker          Ready    <none>          39m   v1.35.0
dpu-sim-host-kind-worker2         Ready    <none>          39m   v1.35.0
$ oc get pods -A -o wide
NAMESPACE            NAME                                                      READY   STATUS    RESTARTS   AGE   IP           NODE                              NOMINATED NODE   READINESS GATES
kube-system          coredns-7d764666f9-89kdk                                  1/1     Running   0          39m   10.244.2.4   dpu-sim-host-kind-worker          <none>           <none>
kube-system          coredns-7d764666f9-w4vb7                                  1/1     Running   0          39m   10.244.2.3   dpu-sim-host-kind-worker          <none>           <none>
kube-system          etcd-dpu-sim-host-kind-control-plane                      1/1     Running   0          39m   10.89.0.6    dpu-sim-host-kind-control-plane   <none>           <none>
kube-system          kube-apiserver-dpu-sim-host-kind-control-plane            1/1     Running   0          39m   10.89.0.6    dpu-sim-host-kind-control-plane   <none>           <none>
kube-system          kube-controller-manager-dpu-sim-host-kind-control-plane   1/1     Running   0          39m   10.89.0.6    dpu-sim-host-kind-control-plane   <none>           <none>
kube-system          kube-scheduler-dpu-sim-host-kind-control-plane            1/1     Running   0          39m   10.89.0.6    dpu-sim-host-kind-control-plane   <none>           <none>
local-path-storage   local-path-provisioner-67b8995b4b-kq54q                   1/1     Running   0          39m   10.244.2.5   dpu-sim-host-kind-worker          <none>           <none>
ovn-kubernetes       ovnkube-control-plane-699dfd94-rrd25                      1/1     Running   0          38m   10.89.0.6    dpu-sim-host-kind-control-plane   <none>           <none>
ovn-kubernetes       ovnkube-identity-x4xsq                                    1/1     Running   0          38m   10.89.0.6    dpu-sim-host-kind-control-plane   <none>           <none>
ovn-kubernetes       ovnkube-node-8c5g2                                        6/6     Running   0          38m   10.89.0.7    dpu-sim-host-kind-worker2         <none>           <none>
ovn-kubernetes       ovnkube-node-pzlvm                                        6/6     Running   0          38m   10.89.0.8    dpu-sim-host-kind-worker          <none>           <none>
ovn-kubernetes       ovnkube-node-qmh9z                                        6/6     Running   0          38m   10.89.0.6    dpu-sim-host-kind-control-plane   <none>           <none>
ovn-kubernetes       ovs-node-bb2j5                                            1/1     Running   0          38m   10.89.0.6    dpu-sim-host-kind-control-plane   <none>           <none>
ovn-kubernetes       ovs-node-lsssr                                            1/1     Running   0          38m   10.89.0.7    dpu-sim-host-kind-worker2         <none>           <none>
ovn-kubernetes       ovs-node-xcghl                                            1/1     Running   0          38m   10.89.0.8    dpu-sim-host-kind-worker          <none>           <none>
$ export KUBECONFIG=./kubeconfig/dpu-sim-dpu-kind.kubeconfig
$ oc get nodes
NAME                             STATUS   ROLES           AGE   VERSION
dpu-sim-dpu-kind-control-plane   Ready    control-plane   38m   v1.35.0
dpu-sim-dpu-kind-worker          Ready    <none>          38m   v1.35.0
dpu-sim-dpu-kind-worker2         Ready    <none>          38m   v1.35.0
$ oc get pods -A -o wide
NAMESPACE            NAME                                                     READY   STATUS    RESTARTS   AGE   IP           NODE                             NOMINATED NODE   READINESS GATES
kube-system          coredns-7d764666f9-2c4ml                                 1/1     Running   0          38m   10.246.1.5   dpu-sim-dpu-kind-worker          <none>           <none>
kube-system          coredns-7d764666f9-8g8jn                                 1/1     Running   0          38m   10.246.1.3   dpu-sim-dpu-kind-worker          <none>           <none>
kube-system          etcd-dpu-sim-dpu-kind-control-plane                      1/1     Running   0          38m   10.89.0.11   dpu-sim-dpu-kind-control-plane   <none>           <none>
kube-system          kube-apiserver-dpu-sim-dpu-kind-control-plane            1/1     Running   0          38m   10.89.0.11   dpu-sim-dpu-kind-control-plane   <none>           <none>
kube-system          kube-controller-manager-dpu-sim-dpu-kind-control-plane   1/1     Running   0          38m   10.89.0.11   dpu-sim-dpu-kind-control-plane   <none>           <none>
kube-system          kube-scheduler-dpu-sim-dpu-kind-control-plane            1/1     Running   0          38m   10.89.0.11   dpu-sim-dpu-kind-control-plane   <none>           <none>
local-path-storage   local-path-provisioner-67b8995b4b-67phd                  1/1     Running   0          38m   10.246.1.4   dpu-sim-dpu-kind-worker          <none>           <none>
ovn-kubernetes       ovnkube-control-plane-699dfd94-j49km                     1/1     Running   0          36m   10.89.0.11   dpu-sim-dpu-kind-control-plane   <none>           <none>
ovn-kubernetes       ovnkube-identity-ktjb8                                   1/1     Running   0          36m   10.89.0.11   dpu-sim-dpu-kind-control-plane   <none>           <none>
ovn-kubernetes       ovnkube-node-pw7nn                                       6/6     Running   0          36m   10.89.0.9    dpu-sim-dpu-kind-worker2         <none>           <none>
ovn-kubernetes       ovnkube-node-s75s7                                       6/6     Running   0          36m   10.89.0.10   dpu-sim-dpu-kind-worker          <none>           <none>
ovn-kubernetes       ovnkube-node-z9bbl                                       6/6     Running   0          36m   10.89.0.11   dpu-sim-dpu-kind-control-plane   <none>           <none>
ovn-kubernetes       ovs-node-bzkkt                                           1/1     Running   0          36m   10.89.0.11   dpu-sim-dpu-kind-control-plane   <none>           <none>
ovn-kubernetes       ovs-node-mzmkz                                           1/1     Running   0          36m   10.89.0.10   dpu-sim-dpu-kind-worker          <none>           <none>
ovn-kubernetes       ovs-node-xzf5h                                           1/1     Running   0          36m   10.89.0.9    dpu-sim-dpu-kind-worker2         <none>           <none>
```

#### Kuberenetes Use Cases with DPU Simulation

With cluster support, you can:

1. **DPU workloads**: Deploy workloads to test DPU offloading
2. **Open vSwitch**: Configure OVS bridges for data plane traffic
3. **Testing**: Test the deployment of DPU-accelerated services

With the multi-cluster support, you can:

1. **Multi-Tenancy Scenarios**: Simulate multiple independent Kubernetes environments
2. **DPU Testing**: Test DPU nodes in either single or dual cluster deployments
3. **Cross-Cluster Communication**: Experiment with DPU Operator orchestration like https://github.com/openshift/dpu-operator which uses OPI APIs

#### Verify Cluster Setup

After installation completes, verify your cluster(s):

#### Single Cluster

```bash
# Check node status
$ export KUBECONFIG=./kubeconfig/cluster-1.kubeconfig
$ kubectl get nodes
NAME       STATUS   ROLES           AGE   VERSION
dpu-1      Ready    <none>          13m   v1.33.6
host-1     Ready    <none>          13m   v1.33.6
master-1   Ready    control-plane   13m   v1.33.6

# Check all pods
$ kubectl get pods -A
...

# Check Flannel CNI
$ kubectl get pods -n kube-flannel

# Or check OVN-Kubernetes CNI
$ kubectl get pods -n ovn-kubernetes

...
```

#### Multiple Clusters

```bash
# Check cluster-1
$ export KUBECONFIG=./kubeconfig/cluster-1.kubeconfig
$ kubectl get nodes

# Check cluster-2
$ export KUBECONFIG=./kubeconfig/cluster-2.kubeconfig
$ kubectl get nodes

# Verify different pod CIDRs
$ export KUBECONFIG=./kubeconfig/cluster-1.kubeconfig
$ kubectl get nodes -o jsonpath="{.items[0].spec.podCIDR}"

$ export KUBECONFIG=./kubeconfig/cluster-1.kubeconfig
$ kubectl get nodes -o jsonpath="{.items[0].spec.podCIDR}"
```

### Manage VMs

List all VMs:
```bash
$ ./bin/vmctl list
VM Name              State           IP Address      vCPUs    Memory
--------------------------------------------------------------------------------
master-1             Running         192.168.120.74  2        4096MB
host-1               Running         192.168.120.66  2        2048MB
dpu-1                Running         192.168.120.69  2        2048MB

```

SSH into a VM:
```bash
$ ./bin/vmctl ssh host-1
Connecting to host-1 (192.168.120.66) as root...
[systemd]
Failed Units: 3
  cloud-final.service
  cloud-init-main.service
  NetworkManager-wait-online.service
[root@host-1 ~]#
```

Start/Stop VMs:
```bash
$ ./bin/vmctl list
VM Name              State           IP Address      vCPUs    Memory
--------------------------------------------------------------------------------
master-1             Running         192.168.120.74  2        4096MB
host-1               Running         192.168.120.66  2        2048MB
dpu-1                Running         192.168.120.69  2        2048MB
$ oc get nodes
NAME       STATUS   ROLES           AGE     VERSION
dpu-1      Ready    <none>          5h49m   v1.33.7
host-1     Ready    <none>          5h49m   v1.33.7
master-1   Ready    control-plane   5h51m   v1.33.7
$ ./bin/vmctl stop dpu-1
✓ Shutting down VM 'dpu-1'...
$ oc get nodes
NAME       STATUS     ROLES           AGE     VERSION
dpu-1      NotReady   <none>          5h49m   v1.33.7
host-1     Ready      <none>          5h49m   v1.33.7
master-1   Ready      control-plane   5h51m   v1.33.7
$ ./bin/vmctl list
VM Name              State           IP Address      vCPUs    Memory
--------------------------------------------------------------------------------
master-1             Running         192.168.120.74  2        4096MB
host-1               Running         192.168.120.66  2        2048MB
dpu-1                Shut off        N/A             2        2048MB
$ ./bin/vmctl start dpu-1
✓ Started VM 'dpu-1'
$ oc get nodes
NAME       STATUS   ROLES           AGE     VERSION
dpu-1      Ready    <none>          5h50m   v1.33.7
host-1     Ready    <none>          5h50m   v1.33.7
master-1   Ready    control-plane   5h52m   v1.33.7
$ ./bin/vmctl list
VM Name              State           IP Address      vCPUs    Memory
--------------------------------------------------------------------------------
master-1             Running         192.168.120.74  2        4096MB
host-1               Running         192.168.120.66  2        2048MB
dpu-1                Running         192.168.120.69  2        2048MB

```

Execute commands remotely:
```bash
$ ./bin/vmctl exec dpu-1 "uname -a"
Linux dpu-1 6.17.1-300.fc43.x86_64 #1 SMP PREEMPT_DYNAMIC Mon Oct  6 15:37:21 UTC 2025 x86_64 GNU/Linux
$ ./bin/vmctl exec dpu-1 "ip link show br-ex"
9: br-ex: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UNKNOWN mode DEFAULT group default qlen 1000
    link/ether 52:54:00:00:01:13 brd ff:ff:ff:ff:ff:ff

```

### Cleanup

Remove all VMs and networks:

```bash
$ ./bin/dpu-sim --cleanup
╔═══════════════════════════════════════════════╗
║               DPU Simulator                   ║
╚═══════════════════════════════════════════════╝
Configuration: config.yaml
Deployment mode: vm

=== Checking Dependencies ===
✓ Detected Linux distribution: rhel 9.6 (package manager: dnf, architecture: x86_64)
✓ wget is installed
✓ pip3 is installed
✓ jinjanator is installed
✓ git is installed
✓ openvswitch is installed
✓ libvirt is installed
✓ qemu-kvm is installed
✓ qemu-img is installed
✓ libvirt-devel is installed
✓ virt-install is installed
✓ genisoimage is installed
✓ All dependencies are available

=== Cleaning up K8s ===
✓ Kubeconfig file removed: kubeconfig/cluster-1.kubeconfig

╔═══════════════════════════════════════════════╗
║       VM-Based Deployment Workflow            ║
╚═══════════════════════════════════════════════╝
=== Cleaning up VMs ===
✓ Deleted disk: /var/lib/libvirt/images/master-1.qcow2
✓ Deleted cloud-init ISO: /var/lib/libvirt/images/master-1-cloud-init.iso
✓ Cleaned up VM: master-1
✓ Deleted disk: /var/lib/libvirt/images/host-1.qcow2
✓ Deleted cloud-init ISO: /var/lib/libvirt/images/host-1-cloud-init.iso
✓ Cleaned up VM: host-1
✓ Deleted disk: /var/lib/libvirt/images/dpu-1.qcow2
✓ Deleted cloud-init ISO: /var/lib/libvirt/images/dpu-1-cloud-init.iso
✓ Cleaned up VM: dpu-1
=== Cleaning up Networks ===
✓ Removed network mgmt-network
✓ Removed network ovn-network
✓ Removed host-to-DPU network h2d-host-1-dpu-1 (bridge: h2d-83d76b0d2f2)

✓ Cleanup complete. No deployment performed.
```

**Warning:** This will permanently delete all VMs and their disks.

**Note:** The `dpu-sim` application automatically cleans up before deploying, so you typically don't need to run cleanup manually unless you just want to remove everything without redeploying.

## VM Access Details

### SSH Access
- **User:** Specified in the config file
- **Authentication:** SSH key (from `~/.ssh/id_rsa`)
- **No password required**

### Console Access (Emergency)
- **User:** Specified in the config file
- **Password:** Specified in the config file (default: "redhat")
- Use console access if SSH is not working

## Project File Structure

```
├── cmd/dpu-sim
│   ├── main.go           # The main application execution for deploying the simulation
├── cmd/vmctl
│   ├── main.go           # The helper application to manage virtual machines
├── pkg/cni
│   ├── flannel.go        # Functions to install Flannel CNI
│   ├── install.go        # Delagates CNI installation
│   ├── multus.go         # Functions to install Multus CNI
│   ├── whereabouts.go    # Functions to install Whereabouts IPAM addon
│   ├── ovn_kubernetes.go # Functions to install OVN-Kubernetes CNI
│   ├── types.go          # Types related to CNI
├── pkg/config
│   ├── config.go         # Functions to manage configuration YAML files
│   ├── config_test.go    # Unit tests for configuration files
│   ├── types.go          # Types related to Config
├── pkg/k8s
│   ├── cleanup.go
│   ├── client.go
│   ├── install.go
│   ├── types.go
├── pkg/kind
│   ├── cleanup.go
│   ├── cluster.go
│   ├── config.go
│   ├── types.go
├── pkg/linux
│   ├── linux.go
├── pkg/log
│   ├── log.go
├── pkg/network
│   ├── network.go       # Bridge name generation & Networking helper functions
│   ├── network_test.go
├── pkg/platform
│   ├── deps.go
│   ├── distro.go
│   ├── distro_test.go
│   ├── executor.go
│   ├── executor_test.go
│   ├── types.go
├── pkg/registry
│   ├── registry.go      # Local Docker registry lifecycle and image loading
├── pkg/requirements
│   ├── requirements.go
├── pkg/ssh
│   ├── ssh.go           # Execute commands on remote hosts
│   ├── ssh_test.go
├── pkg/vm
│   ├── cleanup.go
│   ├──  create.go
│   ├── disk.go
│   ├──  info.go
│   ├── install.go
│   ├── lifecycle.go
│   ├──  network.go
│   ├──  types.go
```

## Testing

### Unit Tests

Each package has unit tests:

```bash
# Run all tests
make test

# Run specific package
go test ./pkg/config/

# Run with coverage
go test -cover ./pkg/config/
```

## Troubleshooting

### VMs not getting IP addresses

Wait 1-2 minutes for VMs to boot. Check VM status:
```bash
$ ./bin/vmctl list
VM Name              State           IP Address      vCPUs    Memory
--------------------------------------------------------------------------------
master-1             Running         192.168.120.51  2        4096MB
master-2             Running         192.168.120.24  2        4096MB
host-1-1             Running         192.168.120.25  2        2048MB
dpu-1-1              Running         192.168.120.94  2        2048MB
host-2-1             Running         192.168.120.69  2        2048MB
dpu-2-1              Running         192.168.120.85  2        2048MB
```

### Cannot connect via SSH

1. Verify VM is running: `./bin/vmctl list`
2. Check VM has IP address
3. Try SSH access: `./bin/vmctl ssh host-1`
4. Verify SSH key exists: `ls -la ~/.ssh/id_rsa*`

### Permission denied errors

Make sure your user is in the `libvirt` group:
```bash
groups | grep libvirt
```

If not, add yourself and log out/in:
```bash
sudo usermod -a -G libvirt $USER
```

### Cannot download cloud image

The download may take time depending on your connection. If it fails:
1. Check internet connectivity
2. Verify the image URL in `config.yaml` is correct
3. Manually download to `/var/lib/libvirt/images/`

### View Cluster Logs

```bash
# Check kubelet logs on any node
./bin/vmctl exec <vm-name> 'sudo journalctl -u kubelet -n 50'

# Check kubeadm init logs on master
./bin/vmctl exec <vm-master-name> 'cat /tmp/kubeadm-init.log'
```

## Open vSwitch Usage

### OVS Data Bridge (on Host system)

If you configured a network with `use_ovs: true`, an OVS bridge is created on the host system that connects all VMs:

```bash
# Check OVS bridge status on host
sudo ovs-vsctl show

# View the data bridge
sudo ovs-vsctl list-br

# Show all ports on the bridge
sudo ovs-vsctl list-ports ovs-data

# View OpenFlow rules
sudo ovs-ofctl dump-flows ovs-data

# Add OpenFlow rules (example: drop all traffic)
sudo ovs-ofctl add-flow ovs-data priority=100,actions=drop

# Delete all flows
sudo ovs-ofctl del-flows ovs-data

# Enable port mirroring (mirror eth1 traffic to eth2)
sudo ovs-vsctl -- set Bridge ovs-data mirrors=@m \
  -- --id=@eth1 get Port eth1 \
  -- --id=@eth2 get Port eth2 \
  -- --id=@m create Mirror name=mirror0 select-all=true output-port=@eth2
```

### OVS Inside VMs

Each VM also has OVS installed for custom networking inside the VM:

```bash
# SSH into any VM
./bin/vmctl ssh host-1

# Check OVS status
sudo ovs-vsctl show

# Create a bridge
sudo ovs-vsctl add-br br0

# List bridges
sudo ovs-vsctl list-br

# Add a port
sudo ovs-vsctl add-port br0 eth1
```

## Design
```
┌─────────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                                      dpu-sim Deployment Flow                                                    │
└─────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘

                                         ┌──────────────────┐
                                         │  User runs       │
                                         │  dpu-sim         │
                                         └────────┬─────────┘
                                                  │
                                                  ▼
                                         ┌──────────────────┐
                                         │ Load config.yaml │
                                         │(config.LoadConfig)│
                                         └────────┬─────────┘
                                                  │
                                                  ▼
                                         ┌──────────────────┐
                                         │EnsureDependencies│◀─── Check: docker, kind, kubectl, etc.
                                         │ (if not skipped) │
                                         └────────┬─────────┘
                                                  │
                                                  ▼
                                         ┌──────────────────┐
                                         │  Clean up stale  │
                                         │  kubeconfigs     │
                                         └────────┬─────────┘
                                                  │
                                                  ▼
                                    ┌─────────────┴─────────────┐
                                    │    Deployment Mode?       │
                                    └─────────────┬─────────────┘
                                                  │
                        ┌─────────────────────────┼─────────────────────────┐
                        │ VM Mode                 │                         │ Kind Mode
                        ▼                         │                         ▼
           ┌────────────────────────┐             │            ┌────────────────────────┐
           │    NewVMManager()      │             │            │   NewKindManager()     │
           │  (connect to libvirt)  │             │            │   (Kind provider)      │
           └───────────┬────────────┘             │            └───────────┬────────────┘
                       │                          │                        │
                       ▼                          │                        ▼
           ┌────────────────────────┐             │            ┌────────────────────────┐
           │    CleanupAll()        │             │            │    CleanupAll()        │
           │  (VMs, Networks, Disks)│             │            │  (Delete Kind clusters)│
           └───────────┬────────────┘             │            └───────────┬────────────┘
                       │                          │                        │
═══════════════════════╪══════════════════════════╪════════════════════════╪═══════════════════════════════════════
                       │  PHASE 1: INFRASTRUCTURE │                        │  PHASE 1: CLUSTER CREATION
═══════════════════════╪══════════════════════════╪════════════════════════╪═══════════════════════════════════════
                       ▼                          │                        ▼
           ┌────────────────────────┐             │            ┌────────────────────────┐
           │  CreateAllNetworks()   │             │            │  DeployAllClusters()   │
           └───────────┬────────────┘             │            └───────────┬────────────┘
                       │                          │                        │
              ┌────────┴────────┐                 │                        │ (for each cluster)
              ▼                 ▼                 │                        ▼
        ┌──────────┐    ┌─────────────┐           │            ┌────────────────────────┐
        │   NAT    │    │  L2-Bridge  │           │            │  BuildKindConfig()     │
        │ Networks │    │  Networks   │           │            │  - Nodes (CP/worker)   │
        │ (DHCP)   │    │ (OVS/Linux) │           │            │  - Pod/Service CIDR    │
        └────┬─────┘    └──────┬──────┘           │            │  - Disable default CNI │
             │                 │                  │            │  - kubeadm patches     │
             └────────┬────────┘                  │            └───────────┬────────────┘
                      │                           │                        │
                      ▼                           │                        ▼
           ┌────────────────────────┐             │            ┌────────────────────────┐
           │Create Host-DPU Networks│             │            │   provider.Create()    │◀─── Kind library creates:
           │  (Implicit OVS links)  │             │            │                        │     - Docker containers
           └───────────┬────────────┘             │            │                        │     - Docker network
                       │                          │            │                        │     - kubeadm init/join
                       ▼                          │            └───────────┬────────────┘
           ┌────────────────────────┐             │                        │
           │    CreateAllVMs()      │             │                        ▼
           └───────────┬────────────┘             │            ┌────────────────────────┐
                       │                          │            │   GetKubeconfig()      │
                       │ (for each VM)            │            │   Save to file         │
                       ▼                          │            └───────────┬────────────┘
           ┌────────────────────────┐             │                        │
           │ CreateVMDisk()         │             │                        ▼
           │ (qemu-img, qcow2)      │             │            ┌────────────────────────┐
           │ CreateCloudInitISO()   │             │            │ InstallDependencies()  │
           │ - meta-data (hostname) │             │            └───────────┬────────────┘
           │ - user-data (SSH, pkg) │             │                        │
           └───────────┬────────────┘             │                        │
                       │                          │                        ▼
                       ▼                          │            ┌────────────────────────┐
           ┌────────────────────────┐             │            │ InstallDependencies()  │
           │  GenerateVMXML()       │             │            │ (IPv6 on each node     │
           │  - CPU, Memory         │             │            │  via docker exec)      │
           │  - Disks (qcow2, ISO)  │             │            └───────────┬────────────┘
           │  - Network interfaces  │             │                        │
           │    (mgmt, k8s, host to │             │                        │
           │    dpu)                │             │                        │
           └───────────┬────────────┘             │                        │
                       │                          │                        │
                       ▼                          │                        │
           ┌────────────────────────┐             │                        │
           │ DomainDefineXML()      │             │                        │
           │ SetAutostart()         │             │                        │
           │ domain.Create()        │◀─── Start  │                        │
           └───────────┬────────────┘     QEMU    │                        │
                       │                          │                        │
                       ▼                          │                        │
           ┌────────────────────────┐             │                        │
           │   WaitForVMIP()        │             │                        │
           │   (DHCP lease, 5min)   │             │                        │
           │   WaitForSSH()         │             │                        │
           │   (SSH ready, 5min)    │             │                        │
           └───────────┬────────────┘             │                        │
                       │                          │                        │
═══════════════════════╪══════════════════════════╪════════════════════════╪═══════════════════════════════════════
                       │   PHASE 2: KUBERNETES    │                        │   PHASE 2: CNI INSTALLATION
═══════════════════════╪══════════════════════════╪════════════════════════╪═══════════════════════════════════════
                       ▼                          │                        ▼
           ┌────────────────────────┐             │            ┌────────────────────────┐
           │ InstallKubernetes()    │             │            │    InstallCNI()        │
           │ (on each VM via SSH)   │             │            │ (for each cluster)     │
           │ - containerd           │             │            └───────────┬────────────┘
           │ - kubeadm, kubelet     │             │                        │
           │ - kubectl              │             │               ┌────────┴────────┐
           └───────────┬────────────┘             │               │                 │
                       │                          │               ▼                 ▼
                       ▼                          │     ┌─────────────────┐  ┌─────────────────┐
           ┌────────────────────────┐             │     │ OVN-Kubernetes? │  │ Other CNI       │
           │ SetupAllK8sClusters()  │             │     │ Yes             │  │ (Flannel, etc.) │
           │ kubeadm init           │             │     └────────┬────────┘  └────────┬────────┘
           └───────────┬────────────┘             │              │                    │
                       │                          │              ▼                    │
                       ▼                          │     ┌─────────────────┐           │
           ┌────────────────────────┐             |     │PullAndLoadImage │           │
           │ Save Kubeconfig        │             │     │(docker pull +   │           │
           └───────────┬────────────┘             │     │ kind load)      │           │
                       │                          │     └────────┬────────┘           │
                       ▼                          │              │                    │
           ┌────────────────────────┐             │              └─────────┬──────────┘
           │ cniMgr.InstallCNI()    │             │                        │
           │ - Helm Install         │             │                        ▼
           │ - Wait for pods ready  │             │            ┌────────────────────────┐
           │ - Delete kube-proxy    │             │            │ GetInternalAPIServerIP │
           │   (if OVN-K8s)         │             │            │ (docker inspect)       │
           └───────────┬────────────┘             │            └───────────┬────────────┘
                       │                          │                        │
           ┌────────────────────────┐             │                        ▼
           │ Join other masters &   │             │            ┌────────────────────────┐
           │ workers via kubeadm    │             │            │ cniMgr.InstallCNI()    │
           └───────────┬────────────┘             │            │ - Helm Install         │
                       │                          │            │ - Wait for pods ready  │
                       │                          │            │ - Delete kube-proxy    │
                       │                          │            │   (if OVN-K8s)         │
                       |                          │            └───────────┬────────────┘
                       |                          │                        │
                       │                          │                        │
═══════════════════════╪══════════════════════════╪════════════════════════╪═══════════════════════════════════════
                       │                          │                        │
                       ▼                          │                        ▼
           ┌────────────────────────┐             │            ┌────────────────────────┐
           │  ✓ VM Deployment       │            │            │  ✓ Kind Deployment     │
           │    Complete!           │             │            │    Complete!           │
           │                        │             │            │                        │
           │  • VMs running         │             │            │  • Clusters running    │
           │  • K8s installed       │             │            │  • CNI deployed        │
           │  • CNI deployed        │             │            │  • Kubeconfigs saved   │
           │  • Kubeconfigs saved   │             │            │                        │
           └────────────────────────┘             │            └────────────────────────┘
                                                  │
                                                  │
```
## License

This project is provided as-is for educational and development purposes.

## Contributing

Feel free to submit issues and enhancement requests!
