# DPU Simulator - VM and Container-based Kubernetes + OVN-Kubernetes CNI Environment

This project automates the deployment of DPU simulation environments using either **VMs (libvirt)** or **containers (Kind)**, pre-configured with Kubernetes and OVN-Kubernetes (or other CNIs) for container networking experiments/development/CI/CD.

DPUs are being used in data centers to accelerate different workloads such as AI (Artificial Intelligence), NFs (Network Functions) and many use cases. This DPU simulation's goal is to bring the DPU into developer's hands without needing the hardware. DPU hardware has limitations such as ease of provisioning, hardware availability, cost, embedded CPU capacity, and others, the DPU simulation tools here using Virtual Machines or Containers should lower the barrier of entry to move fast in developing features in Kubernetes, CNIs, APIs, etc... The second objective is to use this simulation in upstream CI/CD for CNIs that support offloading to DPUs such as OVN-Kubernetes.

These are the list of DPUs that this simulation will try to emulate:
- NVIDIA BlueField 3 (Currently supports OVN-Kubernetes DPU offload)
- Marvell Octeon 10
- Intel NetSec Accelerator
- Intel IPU

All these DPUs have common simularities, some we can emulate better than others. As this DPU simulation project grows there would a increased interest and need to simulate the hardware closely (e.g. eSwitch) in QEMU drivers.

## Status: 🚧 Active Development
 - `dpu-sim` is functional for VM & Kind mode supporting OVN-Kubernetes DPU offload.
 - `vmctl` is functional for managing VMs created by dpu-sim.

## Features

### Core Features

- 🚀 **VM or Kind**: One deployment style per config—libvirt/QEMU guests or Kind node containers (Docker/Podman).
- ☸️ **Kubernetes from YAML**: kubeadm-based control plane and worker join; clusters, roles, and per-cluster CNI install without hand-written bootstrap scripts.
- 🔀 **Primary CNI and addons**: OVN-Kubernetes, Flannel, or Kindnet; optional Multus, Whereabouts, and cert-manager where supported.
- ⚡ **OVN-Kubernetes DPU offload** (optional): `kubernetes.offload_dpu` for split host vs DPU cluster topology with OVN-Kubernetes DPU offload. Examples: `config-ovnk-offload.yaml` (VM), `config-kind-ovnk-offload.yaml` (Kind).
- 🌐 **Networks**: NAT, Kubernetes underlay, Layer 2 bridges, and host-DPU links as configured.
- **Multi-cluster**: Each `kubernetes.clusters[]` entry is a separate API server and kubeconfig; nodes select a cluster with `k8s_cluster`.
- **Custom images**: Optional local registry builds (e.g. custom OVN-K) or Kind `kind load` when the registry is disabled.
- **Optional TFT**: `tft` blocks in config support `dpu-sim tft run` for traffic checks (including OVN-Kubernetes DPU offload).
- 🧹 **Cleanup** for both VM and Kind deployments.

### VM Mode Features
- 🔌 Configurable NIC models (virtio, igb)
- 🖥️ Q35 machine type with PCIe and IOMMU support (SR-IOV ready)
- 🔑 SSH key-based authentication
- 💻 Easy VM access via SSH and console
- 🎛️ Full VM lifecycle management (start, stop, reboot)
- 🔀 Open vSwitch (OVS) for Host-To-DPU networking

### Kind Mode Features
- ⚡ **Fast iteration** - clusters deploy in seconds
- 🐳 Uses Docker/Podman containers instead of VMs
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

For Kind deployments with **`kubernetes.offload_dpu: true`**, the host and DPU
container networks must be routable from the host. Docker and Podman with
host-visible Linux bridge networking are supported. Rootless Podman with
`pasta` is not supported for this topology because it does not expose bridge
interfaces on the host for dpu-sim to configure forwarding between the Kind and
DPU gateway networks. This limitation can be removed once OVN-Kubernetes
provides Kubernetes API reachability through an admin-policy based route.

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
  tft-run              Run TFT via dpu-sim (set CONFIG=path/to.yaml; uses .tft-venv if present)
```

## Configuration

The simulator supports two deployment modes, configured via different sections in the YAML config file:

- **VM Mode**: Uses `vms` section (libvirt-based VMs)
- **Kind Mode**: Uses `kind` section (Docker containers)

### What is OVN-Kubernetes DPU offload?

In production, **OVN-Kubernetes DPU offload** is the pattern where the **host** runs Kubernetes control-plane and “DPU-host” networking components, while the **DPU** runs the data-plane fast path (**Open vSwitch**, OVN integration, representors, and so on). Traffic for pods that are offloaded is steered across the host-DPU link instead of being fully processed on the host NIC networking stack.

**dpu-sim** models that split with **two Kubernetes clusters** in one YAML file (**`kubernetes.clusters`**): workers tagged **`type: host`** belong to the host-side cluster, and **`type: dpu`** workers belong to the DPU-side cluster, with a **`HostToDpu`** network (libvirt + OVS links in VM mode, **veth** links between Kind containers in Kind mode). Set **`kubernetes.offload_dpu: true`** so OVN-Kubernetes is installed in the right **mode per cluster** instead of a single “full” OVN-K on every node:

- **DPU-host cluster** (the cluster that does *not* contain `type: dpu` nodes): OVN-Kubernetes **DPU-host** charts run on the host cluster; ovnkube uses the management / gateway path toward the paired DPU. Workloads that request accelerated ports (for example via **Multus** NADs and the device plugin) are set up so traffic can be steered toward the DPU path.
- **DPU cluster** (the cluster whose workers include `type: dpu`): On DPU nodes, **OVS** is brought up and **`external_ids`** are set the way real DPU nodes expect, then OVN-Kubernetes runs in **DPU** mode alongside whatever **primary CNI** you chose for that cluster. A second primary CNI such as **Flannel** is common so the DPU cluster has its own pod network while OVN-K still handles the offload portion. In the future OVN-Kubernetes will support dual modes meaning that the primary CNI can be OVN-Kubernetes.

With **`offload_dpu`**, dpu-sim can also build and publish a **device plugin** image so simulated “VF” style resources (see **`dpusim.io/vf`** in TFT examples) line up with how upstream OVN-Kubernetes DPU expects **Multus** secondary networks and pod **requests/limits**.

**Examples:** `config-ovnk-offload.yaml` (VM) and `config-kind-ovnk-offload.yaml` (Kind).

### VM Architecture

VM mode runs each node as a **KVM guest** under **libvirt**: dpu-sim defines domains with QEMU, attaches **libvirt networks** (Linux bridge or **Open vSwitch** where configured), and boots a **cloud image** prepared with **cloud-init** (SSH access, interface rename rules, **`k8s`** static addressing, and NetworkManager “unmanaged” MACs so CNIs can own those ports). **Kubernetes and CNI** are installed afterward over **SSH** from the host.

- **Hypervisor and XML**: Domains are **`kvm`** with **`cpu mode='host-passthrough'`** so guests see host CPU features. Disks are **qcow2** on **virtio**; the cloud-init payload is attached as a **SATA CD-ROM** for first boot.
- **x86_64 (`machine='q35'`)**: Guests use the **Q35** chipset, **APIC**, **ACPI**, and an **Intel IOMMU** block suitable for device assignment–style testing and DPU-like topologies.
- **aarch64 (`machine='virt'`)**: Guests use the **`virt`** machine type with **UEFI (pflash)** when firmware is available on the host; the IOMMU XML block used on x86_64 is **not** enabled there.
- **One Kubernetes cluster per `kubernetes.clusters` entry**: The same mapping as Kind mode-each VM’s **`k8s_cluster`** selects which kubeadm cluster it joins. Control plane and workers are distinguished with **`k8s_role`** (`master` / `worker`).
- **Stable node identity on the data plane**: On the network whose **`type` is `k8s`**, the guest NIC uses the MAC from **`k8s_node_mac`** and kubelet is pinned to **`k8s_node_ip`** so the node object matches the subnet plan.
- **`networks` on the host**: For each bridge-backed entry, dpu-sim creates a **libvirt `network`** (NAT or isolated L2) backed by **`bridge_name`**, optionally with **`<virtualport type='openvswitch'/>`** when **`use_ovs: true`**. **`attach_to`** filters which **`type: host`** or **`type: dpu`** VMs get that interface.
- **`HostToDpu`**: For each host–DPU pair and each index **`0 … num_pairs-1`**, dpu-sim creates a **dedicated libvirt network** and **OVS bridge** wiring the two guests; extra virtio NICs (with OVS ports) are added to the domain XML on both sides. This is the VM analogue of the Kind **veth** mesh.
- **Management vs Kubernetes traffic**: **`mgmt`** (and similar) networks are intended for **SSH** and stable reachability. The **`k8s`** network is the underlay CNIs such as **OVN-Kubernetes**. Use **`mgmt`** for interactive access, not the CNI-managed segment.
- **Images and registry**: The **`operating_system`** section drives downloading or reusing the **qcow2** base image. An optional **local container registry** on the host builds and serves images (for example **OVN-Kubernetes**); nodes pull from it during CNI install. With **`kubernetes.offload_dpu`**, a **device plugin** image can be built and published the same way.
- **Install path**: After VMs are up, dpu-sim uses **SSH** (see **`ssh`**) to run **kubeadm** / **kubelet** bootstrap and then installs the configured **CNI** and **addons** per cluster. **OVN-Kubernetes DPU offload** follows the same split host / DPU cluster idea as Kind: OVS and **`external_ids`** on DPU-side nodes mirror real DPU bring-up.

### Kind Architecture

Kind mode models the same **split host / DPU Kubernetes** idea as VM mode, but each node runs as a **container** (Docker or Podman) managed by [Kind](https://kind.sigs.k8s.io/), not as a libvirt/QEMU guest.

- **One cluster per `kubernetes.clusters` entry**: dpu-sim builds a separate Kind cluster for each configured cluster name. Nodes are filtered by `k8s_cluster` when the Kind config is generated.
- **Stable logical names**: Kind does not let you rename nodes, so dpu-sim applies the label **`dpu-sim.org/node-name=<config name>`** on each node. Use that label in `kubectl` to map a config `name` to the actual node object.
- **In-cluster networking**: Nodes join the default Kind bridge; **`eth0`** is the primary interface Kind uses for the Kubernetes API and pod CNI plumbing. For custom CNIs, the default Kind CNI is disabled (`kindnet` is the exception); **kube-proxy** is turned off when the primary CNI is OVN-Kubernetes (OVN handles service routing).
- **`HostToDpu` (`networks`)**: After clusters exist, dpu-sim creates **veth data channels** between paired host and DPU **containers** (pairs can span two Kind clusters). Per pair you get `num_pairs` links (e.g. `eth0-*` on the host node container and `rep0-*` on the DPU side). With **`kubernetes.offload_dpu`**, an extra **gateway / management-style veth** is also wired so DPU-host mode can reach the DPU path.
- **Images and registry**: Optional **local registry** is attached to the Kind container network so nodes pull custom builds (for example OVN-Kubernetes). If the registry is disabled in Kind mode, images are **built and loaded into Kind** (`kind load`) instead. DPU offload can also pull/load a **device plugin** image when needed for **`kubernetes.offload_dpu`**.
- **OVN-Kubernetes DPU offload on Kind**: On DPU worker nodes, dpu-sim installs **Open vSwitch inside the DPU container** and sets **`external_ids`** the way the VM flow does on real DPU hardware, then installs the primary CNI (and OVN-Kubernetes in DPU mode when the topology requires it).

### VM Mode Configuration

VM mode is selected when the file defines **`vms`** and does not combine them with a Kind topology (you cannot set both `vms` and `kind.nodes`). Whenever **`vms`** is non-empty, **`operating_system`** is required as described below.

#### Networks (`networks`)

Every network needs **`name`** and **`type`**. **`nic_model`** defaults to **`virtio`** if omitted.

**Bridge-backed networks** (any `type` other than `HostToDpu`, for example `mgmt`, `k8s`, or `layer2`):

| Field | Required | Default | Notes |
|-------|----------|---------|--------|
| `name` | Yes | - | |
| `type` | Yes | - | Drives how dpu-sim creates the libvirt network (mgmt, k8s, layer2)|
| `bridge_name` | Yes | - | This is the name of the bridge from the host point of view. |
| `mode` | No | `nat` | `nat` or `l2-bridge` |
| `use_ovs` | No | `false` | When `true`, libvirt uses an Open vSwitch virtual port instead of Linux bridging |
| `attach_to` | No | `any` | `any`, `host`, or `dpu`: which VM `type` gets this NIC |
| `nic_model` | No | `virtio` | `virtio`, `igb` or any virtual NIC that libvirt QEMU supports |
| `gateway`, `subnet_mask`, `dhcp_start`, `dhcp_end` | No | - | Used for managed/NAT-style networks as in the example |
| `num_pairs` | - | - | Must **not** be set (only valid for `HostToDpu`) |

**Host To Dpu networks** (`type: HostToDpu`):

| Field | Required | Default | Notes |
|-------|----------|---------|--------|
| `name`, `type` | Yes | - | |
| `num_pairs` | Recommended | `1` if omitted or ≤ 0 | Number of parallel host–DPU links per pair (libvirt/OVS in VM mode) |
| `nic_model` | No | `virtio` | `virtio` is recommended |
| `bridge_name`, `gateway`, `subnet_mask`, `dhcp_start`, `dhcp_end`, `mode`, `use_ovs`, `attach_to` | - | - | Must **not** be set (will result in validation error) |

##### Network Types

Network types change the behaviour of dpu-sim on how they treat the network. For example "k8s" network shouldn't be used to access machines, rather the "mgmt" network should be used (more stable/non-changing)

- **`mgmt`**: A non-changing network to provide SSH access to the machine
- **`k8s`**: A network that the CNI would have access to. For example OVN-Kubernetes would have control of this network and it's interfaces.
- **`layer2`**: A network that is layer 2 connection between 2 machines. Currently dpu-sim does not modify this network beyond configuring it.

##### Network Modes

- **`nat`**: VMs can communicate with each other AND access the internet via NAT (requires gateway, subnet_mask, dhcp_start, dhcp_end)
- **`l2-bridge`**: Pure Layer 2 bridge - VMs connected like a switch, no IP/DHCP management (configure IPs manually in VMs)
  - Set `use_ovs: true` to use Open vSwitch (recommended) instead of Linux bridge

##### Network Attachment

The `attach_to` field controls which VM types a network should attach to:

- **`any`** (default): Attach to all VMs regardless of type
- **`host`**: Only attach to VMs with `type: host`
- **`dpu`**: Only attach to VMs with `type: dpu`

Example use case: You might want a management network attached to all VMs, but a specific data plane network only attached to DPU VMs.

##### NIC Models

- **`virtio`**: High-performance paravirtualized NIC (recommended for management)
- **`igb`**: Intel 82576 Gigabit Ethernet emulation (good for testing Intel drivers)
- **`e1000`**: Intel PRO/1000 emulation (widely compatible)
- **`e1000e`**: Intel 82574 emulation (newer than e1000)
- **`rtl8139`**: Realtek 8139 emulation

These NICs come from libvirt QEMU support directly.

#### VMs (`vms`)

Each VM entry:

| Field | Required | Notes |
|-------|----------|--------|
| `name` | Yes | Logical / libvirt name |
| `type` | Yes | `host` or `dpu` |
| `k8s_cluster` | Yes | Must match a `kubernetes.clusters[].name` |
| `k8s_role` | Yes | Use `master` or `worker` (not Kind’s `control-plane`); each cluster needs at least one `master` |
| `k8s_node_mac` | Yes | Applied to the interface on the network whose `type` is `k8s` |
| `k8s_node_ip` | Yes | Kubernetes node IP on that `k8s` network (kubelet `--node-ip`, etc.) |
| `memory` | Yes | Unit of MB; must be > 0 |
| `vcpus` | Yes | Must be > 0 |
| `disk_size` | Yes | Unit of GB; must be > 0 |
| `host` | Yes for `type: dpu` | `name` of the attached `type: host` VM |

#### VM Operating System (`operating_system`) (required when `vms` is set)

| Field | Required | Notes |
|-------|----------|--------|
| `image_name` | Yes | Local filename for the cloud image |
| `image_url` *or* `image_ref` | Yes (one of) | Mutually exclusive: download URL or existing image reference |

#### VM SSH Access (`ssh`)

| Field | Required | Default | Notes |
|-------|----------|---------|--------|
| `user` | No | `root` | |
| `password` | No | `redhat` | |
| `key_path` | No | - | Tilde in paths is expanded |

#### Kubernetes `kubernetes`

| Field | Required | Default | Notes |
|-------|----------|---------|--------|
| `version` | No | `1.33` | |
| `kubeconfig_dir` | No | `kubeconfig` | |
| `offload_dpu` | No | `false` | OVN-Kubernetes DPU offload setup when `true` |
| `clusters` | Yes | - | At least one cluster; OVN-Kubernetes DPU offload uses **two** |

Each **`kubernetes.clusters[]`** entry:

| Field | Required | Default | Notes |
|-------|----------|---------|--------|
| `name` | Yes | - | |
| `cni` | Yes | - | `ovn-kubernetes`, `flannel`, or `kindnet` |
| `pod_cidr` | No | `10.244.0.0/16` | |
| `service_cidr` | No | `10.245.0.0/16` | |
| `addons` | No | - | `multus`, `whereabouts`, `cert-manager` |

#### Registry Section (`registry`) (optional)

If the `registry` section is present with **`containers`**, each item needs **`name`**, **`cni`** (must be `ovn-kubernetes` for image builds), and **`tag`**. VM mode does not support **`registry.enabled: false`** together with **`registry.containers`** (that path is Kind-only).

#### Traffic Flow Tests (`tft`) and top-level (`kubeconfig`) (optional)

Used for `dpu-sim tft run`; not required for VM bring-up.

#### Example Configuration for VM-Mode with OVN-Kubernetes DPU offload

Edit `config-ovnk-offload.yaml` for the OVN-Kubernetes DPU offload VM layout below, or start from `config.yaml` for a simpler topology.

```yaml
networks:
  - name: "mgmt-network"
    type: "mgmt"
    bridge_name: "virbr-mgmt"
    gateway: "192.168.120.1"
    subnet_mask: "255.255.255.0"
    dhcp_start: "192.168.120.10"
    dhcp_end: "192.168.120.100"
    mode: "nat"
    nic_model: "virtio"
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

  - name: "data-l2-network"
    type: "layer2"
    bridge_name: "ovs-data"
    mode: "l2-bridge"
    nic_model: "virtio"
    use_ovs: true
    attach_to: "dpu"

  - name: "host-to-dpu-link"
    type: "HostToDpu"
    num_pairs: 16
    nic_model: "virtio"

vms:
  - name: "master-1"
    type: "host"
    k8s_cluster: "dpu-sim-host"
    k8s_role: "master"
    k8s_node_mac: "52:54:00:00:01:11"
    k8s_node_ip: "192.168.123.11"
    memory: 4096  # MB
    vcpus: 2
    disk_size: 20  # GB

  - name: "master-2"
    type: "host"
    k8s_cluster: "dpu-sim-dpu"
    k8s_role: "master"
    k8s_node_mac: "52:54:00:00:02:11"
    k8s_node_ip: "192.168.123.21"
    memory: 4096  # MB
    vcpus: 2
    disk_size: 20  # GB

  - name: "host-1-1"
    type: "host"
    k8s_cluster: "dpu-sim-host"
    k8s_role: "worker"
    k8s_node_mac: "52:54:00:00:01:12"
    k8s_node_ip: "192.168.123.12"
    memory: 2048  # MB
    vcpus: 2
    disk_size: 20  # GB

  - name: "dpu-1-1"
    type: "dpu"
    k8s_cluster: "dpu-sim-dpu"
    k8s_role: "worker"
    k8s_node_mac: "52:54:00:00:02:12"
    k8s_node_ip: "192.168.123.22"
    host: "host-1-1"
    memory: 4096  # MB
    vcpus: 2
    disk_size: 20  # GB

  - name: "host-2-1"
    type: "host"
    k8s_cluster: "dpu-sim-host"
    k8s_role: "worker"
    k8s_node_mac: "52:54:00:00:01:13"
    k8s_node_ip: "192.168.123.13"
    memory: 2048  # MB
    vcpus: 2
    disk_size: 20  # GB

  - name: "dpu-2-1"
    type: "dpu"
    k8s_cluster: "dpu-sim-dpu"
    k8s_role: "worker"
    k8s_node_mac: "52:54:00:00:02:13"
    k8s_node_ip: "192.168.123.23"
    host: "host-2-1"
    memory: 4096  # MB
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
  offload_dpu: true
  clusters:
    - name: "dpu-sim-host"
      pod_cidr: "10.244.0.0/16"
      service_cidr: "10.245.0.0/16"
      cni: "ovn-kubernetes"
      addons:
        - multus
    - name: "dpu-sim-dpu"
      pod_cidr: "10.246.0.0/16"
      service_cidr: "10.247.0.0/16"
      cni: "flannel"
      addons:
        - multus

registry:
  containers:
    - name: "ovn-kube"
      cni: "ovn-kubernetes"
      tag: "ovn-kube:dpu-sim"

tft:
  - name: "Test 1"
    namespace: "default"
    test_cases: "1-24"
    duration: "10"
    pre_provision: "true"
    connections:
      - name: "Connection_1"
        type: "iperf-tcp"
        instances: 1
        server:
          - name: "host-1-1"
            persistent: "false"
            sriov: "true"
            default_network: "default/ovn-primary"
        client:
          - name: "host-2-1"
            sriov: "true"
            default_network: "default/ovn-primary"
        resource_name: "dpusim.io/vf"
kubeconfig: "kubeconfig/dpu-sim-host.kubeconfig"
```

### Kind Mode Configuration

Kind mode is selected when the file defines **`kind.nodes`** (non-empty) and does **not** define **`vms`** (or bare-metal nodes) for the same deployment. You do **not** need **`operating_system`** or VM sizing fields: each Kubernetes node is a **Kind** node container on Docker or Podman.

#### Networks (`networks`)

Validation uses the **same rules** as VM mode. Typical Kind-only configs list only **`HostToDpu`**; dpu-sim uses that entry to drive **veth data channels** between host and DPU node containers after the clusters are created. Bridge-backed networks (`mgmt`, `k8s`, `layer2`, ...) are **not** attached to Kind nodes by dpu-sim today.

**Host To Dpu networks** (`type: HostToDpu`):

| Field | Required | Default | Notes |
|-------|----------|---------|--------|
| `name`, `type` | Yes | - | |
| `num_pairs` | Recommended | `1` if omitted or ≤ 0 | Parallel data channels per host–DPU pair |
| `nic_model`, `bridge_name`, `gateway`, `subnet_mask`, `dhcp_start`, `dhcp_end`, `mode`, `use_ovs`, `attach_to` | - | - | Must **not** be set |

#### Kind Nodes (`kind.nodes`)

Every Kind node entry:

| Field | Required | Notes |
|-------|----------|--------|
| `name` | Yes | Logical name; written as node label **`dpu-sim.org/node-name`** (Kind cannot rename nodes) |
| `k8s_cluster` | Yes | Must match a **`kubernetes.clusters[].name`** |
| `k8s_role` | Yes | **`control-plane`** or **`worker`** (use these strings, not VM’s `master`) |
| `type` | No | For workers: **`host`** or **`dpu`**. Omit for control-plane nodes |
| `host` | Yes for `type: dpu` | **`name`** of the paired **`type: host`** worker in `kind.nodes` |

Each cluster in **`kubernetes.clusters`** must have at least one **`control-plane`** node with matching **`k8s_cluster`**. For DPU offload topologies, include **`type: host`** and **`type: dpu`** workers and pair each DPU with **`host: <host worker name>`**.

#### Operating System and SSH Access (`operating_system` and `ssh`)

Currently not used for Kind node provisioning . You may omit **`operating_system`**. **`ssh`**. Kind nodes are reached with **`docker exec` / `kubectl`**, not SSH from this config. All Kind nodes use a common base container image.

#### Kubernetes (`kubernetes`)

| Field | Required | Default | Notes |
|-------|----------|---------|--------|
| `version` | No | `1.33` | |
| `kubeconfig_dir` | No | `kubeconfig` | |
| `offload_dpu` | No | `false` | OVN-Kubernetes DPU offload setup when `true` |
| `clusters` | Yes | - | At least one cluster; OVN-Kubernetes DPU offload uses **two** |

Each **`kubernetes.clusters[]`** entry:

| Field | Required | Default | Notes |
|-------|----------|---------|--------|
| `name` | Yes | - | |
| `cni` | Yes | - | `ovn-kubernetes`, `flannel`, or `kindnet` |
| `pod_cidr` | No | `10.244.0.0/16` | Must not overlap between clusters you run in parallel |
| `service_cidr` | No | `10.245.0.0/16` | |
| `addons` | No | - | `multus`, `whereabouts`, `cert-manager` |

#### Registry Section (`registry`) (optional)

If **`registry.containers`** is set, each item needs **`name`**, **`cni`** (`ovn-kubernetes` for OVN-K builds), and **`tag`**. **Kind-only:** you may set **`registry.enabled: false`** so images are **built and loaded into Kind** (`kind load`) instead of pushed to a local registry (VM mode does not support that combination).

#### Traffic Flow Tests (`tft`) and top-level (`kubeconfig`) (optional)

Same as VM mode: used for **`dpu-sim tft run`**, not required for cluster bring-up.

#### Example Configuration for Kind-Mode with OVN-Kubernetes DPU offload

Edit **`config-kind-ovnk-offload.yaml`** for the two-cluster OVN-Kubernetes DPU offload layout below, or **`config-kind.yaml`** for a simpler Kind topology.

```yaml
networks:
  - name: "host-to-dpu-link"
    type: "HostToDpu"
    num_pairs: 16

# Kind cluster configuration: one Kind cluster per kubernetes.clusters entry.
# Node "name" is applied as a label (dpu-sim.org/node-name); Kind does not support node renaming.
kind:
  nodes:
    - name: "control-plane-host"
      k8s_role: "control-plane"
      k8s_cluster: "dpu-sim-host"
    - name: "control-plane-dpu"
      k8s_role: "control-plane"
      k8s_cluster: "dpu-sim-dpu"
    - name: "host-1-1"
      type: host
      k8s_role: "worker"
      k8s_cluster: "dpu-sim-host"
    - name: "dpu-1-1"
      type: dpu
      k8s_role: "worker"
      k8s_cluster: "dpu-sim-dpu"
      host: "host-1-1"
    - name: "host-2-1"
      type: host
      k8s_role: "worker"
      k8s_cluster: "dpu-sim-host"
    - name: "dpu-2-1"
      type: dpu
      k8s_role: "worker"
      k8s_cluster: "dpu-sim-dpu"
      host: "host-2-1"

kubernetes:
  version: "1.33"
  offload_dpu: true
  clusters:
    - name: "dpu-sim-host"
      pod_cidr: "10.244.0.0/16"
      service_cidr: "10.245.0.0/16"
      cni: "ovn-kubernetes"
      addons:
        - multus

    - name: "dpu-sim-dpu"
      pod_cidr: "10.246.0.0/16"
      service_cidr: "10.247.0.0/16"
      cni: "flannel"
      addons:
        - multus

registry:
  enabled: false
  containers:
    - name: "ovn-kube"
      cni: "ovn-kubernetes"
      tag: "ovn-kube:dpu-sim"

tft:
  - name: "Test 1"
    namespace: "default"
    test_cases: "1-24"
    duration: "10"
    pre_provision: "true"
    connections:
      - name: "Connection_1"
        type: "iperf-tcp"
        instances: 1
        server:
          - name: "dpu-sim-host-worker"
            persistent: "false"
            sriov: "true"
            default_network: "default/ovn-primary"
        client:
          - name: "dpu-sim-host-worker2"
            sriov: "true"
            default_network: "default/ovn-primary"
        resource_name: "dpusim.io/vf"
kubeconfig: "kubeconfig/dpu-sim-host.kubeconfig"

```

To resolve a config **`name`** to a node object after deploy, use the kubeconfig for that cluster, for example:

```bash
kubectl --kubeconfig kubeconfig/dpu-sim-host.kubeconfig get nodes -l dpu-sim.org/node-name=host-1-1
```

**TFT / traffic tests:** connection endpoints in **`tft`** often reference **Kubernetes node names** as seen by the API server. On Kind those names are **auto-generated** (for example `dpu-sim-host-worker`), not always the same string as **`kind.nodes[].name`**. Use **`kubectl get nodes --show-labels`** (or map via **`dpu-sim.org/node-name`**) when filling in **`server`/`client` `name`** fields.

### Kubernetes

Kubernetes is the default way dpu-sim orchestrates nodes. To bring up machines **without** installing Kubernetes, pass **`--skip-k8s`** to `dpu-sim` (**VM mode** only today).

When Kubernetes is enabled, dpu-sim installs clusters according to **`kubernetes`** in your YAML.

**VM mode:** each **`vms`** entry must set **`k8s_cluster`** to a **`kubernetes.clusters[].name`**, and **`k8s_role`** is **`master`** (control plane) or **`worker`**.

**Kind mode:** each **`kind.nodes`** entry must set **`k8s_cluster`** the same way; **`k8s_role`** must be **`control-plane`** or **`worker`** (Kind’s kubeadm role strings).

Everything Kubernetes-related lives under **`kubernetes`**. Default **`version`** is **1.33** (override with **`kubernetes.version`**). Admin kubeconfigs are written under **`kubeconfig_dir`** (default **`kubeconfig`**, one file per cluster name). Each **`kubernetes.clusters[]`** entry defines:
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
  offload_dpu: true
  clusters:
    - name: "dpu-sim-host"
      pod_cidr: "10.244.0.0/16" # First cluster pod network
      service_cidr: "10.245.0.0/16"
      cni: "ovn-kubernetes"
      addons:
        - multus

    - name: "dpu-sim-dpu"
      pod_cidr: "10.246.0.0/16" # Second cluster pod network
      service_cidr: "10.247.0.0/16"
      cni: "flannel"
      addons:
        - multus

vms:
  - name: "master-1"
    k8s_cluster: "dpu-sim-host"
    k8s_role: "master"
    ...

  - name: "master-2"
    k8s_cluster: "dpu-sim-dpu"
    k8s_role: "master"
    ...

  - name: "host-1"
    k8s_cluster: "dpu-sim-host"
    k8s_role: "worker"
    ...

  - name: "dpu-1"
    k8s_cluster: "dpu-sim-dpu"
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

This enables an empty local registry (no CNI image builds), useful when you want to push your own images.

For hybrid setups, you can override which registry endpoints nodes trust as insecure HTTP registries:

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

The OVN-Kubernetes source code is automatically cloned into `ovn-kubernetes/` when needed. To point dpu-sim at a separate checkout (e.g. an OVN-Kubernetes PR), use `--ovn-kubernetes-path`:

```bash
./bin/dpu-sim --ovn-kubernetes-path /path/to/ovn-kubernetes
```

Similarly, the kubernetes-traffic-flow-tests repository is automatically cloned into `kubernetes-traffic-flow-tests/` when needed. To use a separate checkout, pass `--tft-repo-path` on `dpu-sim tft` (it applies to `tft run` and `tft venv`):

```bash
./bin/dpu-sim tft run --tft-repo-path /path/to/kubernetes-traffic-flow-tests
```

To work on OVN-Kubernetes changes:

```bash
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

# Use Kind mode explicitly (simple two-cluster example)
$ ./bin/dpu-sim --config config-kind.yaml

# Kind + OVN-Kubernetes DPU offload (matches Step 2b in this README)
$ ./bin/dpu-sim --config config-kind-ovnk-offload.yaml

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
  dpu-sim [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  tft         Kubernetes traffic flow tests

Flags:
      --cleanup            Only cleanup existing resources, do not deploy
      --config string      Path to configuration file (default "config.yaml")
  -h, --help               help for dpu-sim
      --log-level string   Log level (error, warn, info, debug) (default "info")
      --rebuild-cni        Rebuild the OVN-Kubernetes CNI image and exit
      --redeploy-cni       Redeploy the OVN-Kubernetes CNI image onto each cluster and exit
      --skip-cleanup       Skip cleanup of existing resources
      --skip-deploy        Skip VM/Kind deployment
      --skip-deps          Skip dependency checks
      --skip-k8s           Skip Kubernetes (VM only) and CNI installation

Use "dpu-sim [command] --help" for more information about a command.
dpu-sim total time: 1ms

# After deployment, use the cluster
$ export KUBECONFIG=kubeconfig/cluster-1.kubeconfig
$ kubectl get nodes
$ kubectl get pods -A
```

### OVN-Kubernetes Values-Only Mode

Use `--ovnk-mode values-only` when you want dpu-sim to create the VM or Kind
DPU environment but leave the OVN-Kubernetes Helm install to another workflow.
In this mode dpu-sim still creates the clusters, host-to-DPU links, node labels,
DPU OVS setup, generated images, addons, and device plugin resources, but it
stops before installing OVN-Kubernetes.

```bash
$ ./bin/dpu-sim \
    --config config-kind-ovnk-offload.yaml \
    --ovnk-mode values-only
```

dpu-sim writes DPU-specific Helm values under `kubeconfig/helm-values/`, for
example:

```text
kubeconfig/helm-values/dpu-sim-host-ovn-kubernetes-dpu-host-values.yaml
kubeconfig/helm-values/dpu-sim-dpu-ovn-kubernetes-dpu-values.yaml
```

Use these files as the last values file in your OVN-Kubernetes Helm command so
the DPU-specific settings override earlier defaults:

```bash
$ kubectl --kubeconfig kubeconfig/dpu-sim-host.kubeconfig apply \
    -f https://raw.githubusercontent.com/kubernetes-sigs/network-policy-api/v0.1.5/config/crd/experimental/policy.networking.k8s.io_adminnetworkpolicies.yaml

$ kubectl --kubeconfig kubeconfig/dpu-sim-host.kubeconfig apply \
    -f https://raw.githubusercontent.com/kubernetes-sigs/network-policy-api/v0.1.5/config/crd/experimental/policy.networking.k8s.io_baselineadminnetworkpolicies.yaml

$ helm upgrade --install ovn-kubernetes /path/to/ovn-kubernetes/helm/ovn-kubernetes \
    --kubeconfig kubeconfig/dpu-sim-host.kubeconfig \
    -f /path/to/your/normal-values.yaml \
    -f kubeconfig/helm-values/dpu-sim-host-ovn-kubernetes-dpu-host-values.yaml
```

Helm installs the OVN-Kubernetes CRDs bundled with the chart on fresh installs.
AdminNetworkPolicy and BaselineAdminNetworkPolicy are external Network Policy
API CRDs, so values-only users must apply them separately when admin network
policy is enabled. Make sure your normal values also set
`global.enableAdminNetworkPolicy=true` if you need ANP/BANP support.

After the host-side OVN-Kubernetes install exists, create host access resources
and regenerate DPU values if DPU-side components need host-cluster credentials:

```bash
$ ./bin/dpu-sim ovnk host-access \
    --config config-kind-ovnk-offload.yaml \
    --cluster dpu-sim-host

$ ./bin/dpu-sim ovnk values \
    --config config-kind-ovnk-offload.yaml \
    --cluster dpu-sim-dpu \
    --require-host-credentials
```

For BGP/FRR-K8S use cases, the DPU values command also writes
`kubeconfig/helm-values/<dpu-cluster>-frr-k8s.env` and a host kubeconfig for
DPU-side FRR-K8S. Source that environment file before running a compatible FRR
or OVN-Kubernetes development workflow.

### VM/Kind Mode Usage (OVN-Kubernetes DPU offload case)

#### Step 1: Ensuring dpu-sim is compiled

Binaries are located by default in `bin`. Make sure dpu-sim compiles sucessfully with the go compiler.

#### Step 2a: Deploy (VM)

Deploy all Host and DPU VMs and the network:

```bash
$ ./bin/dpu-sim
╔═══════════════════════════════════════════════╗
║               DPU Simulator                   ║
╚═══════════════════════════════════════════════╝
Configuration: config-ovnk-offload.yaml
Deployment mode: vm

=== Checking Dependencies ===
✓ Detected Linux distribution: rhel 9.6 (package manager: dnf, architecture: x86_64)
✓ wget is installed
# ... (libvirt, qemu, virt-install, genisoimage, etc.)
✓ All dependencies are available

=== Cleaning up K8s ===
✓ Kubeconfig file removed: kubeconfig/dpu-sim-dpu.kubeconfig
✓ Kubeconfig file removed: kubeconfig/dpu-sim-host.kubeconfig

=== Setting up Local Container Registry ===
Starting local container registry...
Registry container dpu-sim-registry is already running
Using cached OVN-Kubernetes image ovn-kube:dpu-sim-a10d870f9d495f81
Tagging ovn-kube:dpu-sim -> localhost:5000/ovn-kube:dpu-sim
Pushing localhost:5000/ovn-kube:dpu-sim to local registry...
# ... (skopeo/podman layer copy lines omitted)
Pushed localhost:5000/ovn-kube:dpu-sim to local registry
Registry setup complete
Building Device Plugin image dpu-sim-dp:latest (Architecture=x86_64)...
# ... (container build steps omitted)
✓ Device Plugin image built: dpu-sim-dp:latest
Tagging dpu-sim-dp:latest -> localhost:5000/dpu-sim-dp:latest
Pushing localhost:5000/dpu-sim-dp:latest to local registry...
# ... (layer copy lines omitted)
Pushed localhost:5000/dpu-sim-dp:latest to local registry

╔═══════════════════════════════════════════════╗
║       VM-Based Deployment Workflow            ║
╚═══════════════════════════════════════════════╝
=== Cleaning up VMs ===
✓ Cleaned up VM: master-1
✓ Cleaned up VM: master-2
# ... (host-1-1, dpu-1-1, host-2-1, dpu-2-1: disk / cloud-init / NVRAM lines)
=== Cleaning up Networks ===
✓ Removed network mgmt-network
✓ Removed network ovn-network
✓ Removed network data-l2-network
✓ Removed network host-to-dpu-link
# ... (per-pair HostToDpu OVS bridges: many lines when num_pairs is large)

=== Deploying VMs ===
=== Creating Networks ===
✓ Created network: mgmt-network
✓ Created network: ovn-network
✓ Created OVS bridge: ovs-data
✓ Created network: data-l2-network
# ... (host-to-DPU OVS bridges and networks for each pair × num_pairs)
✓ All networks created successfully
=== Creating All VMs ===
=== Creating VM: master-1 ===
✓ Image already exists at /var/lib/libvirt/images/Fedora-x86_64.qcow2, skipping download
✓ Created disk for master-1: /var/lib/libvirt/images/master-1.qcow2
✓ Created cloud-init ISO: /var/lib/libvirt/images/master-1-cloud-init.iso
✓ Created and started VM: master-1
# ... (master-2, host-1-1, dpu-1-1, host-2-1, dpu-2-1: same pattern)
✓ All VMs created successfully

=== Waiting for VMs to boot and get IPs ===
Waiting for master-1 to get an IP address...
✓ master-1 IP: 192.168.120.51
Waiting for SSH on master-1...
✓ SSH ready on master-1, waiting for cloud-init to finish...
✓ cloud-init finished on master-1 (status: done)
# ... (remaining nodes: IP, SSH, cloud-init)
Assigned 192.168.123.254/24 to eth0-0 on host-1-1
Assigned 192.168.123.253/24 to eth0-0 on host-2-1

=== Installing Kubernetes and CNI ===
=== Installing Kubernetes on VM-based deployment ===
--- Installing Kubernetes on master-1 (192.168.120.51) ---
Installing Kubernetes on master-1 (ssh://root@192.168.120.51)...
✓ Hostname set to master-1
✓ Detected Linux distribution: fedora 43 (package manager: dnf, architecture: x86_64)
✓ Disable firewalld is installed
Installing missing dependencies: Swap Off, K8s Kernel Modules, crio, openvswitch, NetworkManager-ovs, Kubelet Tools
# ... (per-component install lines on this node)
✓ All dependencies are available
✓ Kubernetes 1.33 installed on master-1
# ... (master-2, host-1-1, dpu-1-1, host-2-1, dpu-2-1: same dependency + kubelet pattern)

=== Setting up Kubernetes cluster dpu-sim-host ===

=== Initializing first control plane node: master-1 ===
Initializing control plane on master-1 (ssh://root@192.168.120.51)...
K8s IP: 192.168.120.51 Pod CIDR: 10.244.0.0/16, Service CIDR: 10.245.0.0/16
Setting up kubectl on master-1 (ssh://root@192.168.120.51)...
✓ Control plane initialized on master-1
# ... (kubeadm worker join and control-plane join commands)
API server endpoint: https://192.168.120.51:6443
✓ Kubeconfig saved to: kubeconfig/dpu-sim-host.kubeconfig
=== Joining worker nodes ===
✓ Worker node joined to Kubernetes cluster: host-1-1
✓ Worker node joined to Kubernetes cluster: host-2-1

=== Aligning kubelet node-ip with k8s_node_ip (OVN underlay) ===
kubelet --node-ip set to 192.168.123.12 (ssh://root@192.168.120.25)
kubelet --node-ip set to 192.168.123.13 (ssh://root@192.168.120.69)
kubelet --node-ip set to 192.168.123.11 (ssh://root@192.168.120.51)

=== Installing ovn-kubernetes CNI on cluster dpu-sim-host ===
Installing OVN-Kubernetes (mode=dpu-host): Pod CIDR: 10.244.0.0/16, Service CIDR: 10.245.0.0/16, API Server: https://192.168.123.11:6443
Patching CoreDNS configmap for OVN-Kubernetes compatibility, dns server: 8.8.8.8
✓ CoreDNS configmap patched successfully
OVN-Kubernetes Helm image: 192.168.120.1:5000/ovn-kube:dpu-sim
Labeling nodes for single-node-zone interconnect...
✓ All nodes labeled for single-node-zone interconnect
✓ Master nodes labeled for OVN-Kubernetes HA
Labeling DPU-host nodes in cluster dpu-sim-host...
✓ DPU-host nodes labeled in cluster dpu-sim-host
Deploying Device Plugin DaemonSet (image=192.168.120.1:5000/dpu-sim-dp:latest)...
Waiting for pods in namespace: kube-system label: app=dpu-sim-device-plugin to be ready...
✓ Pods in namespace: kube-system label: app=dpu-sim-device-plugin are ready
✓ Device Plugin DaemonSet is ready
Running Helm install for OVN-Kubernetes (mode=dpu-host, chart: /root/dpu-sim/ovn-kubernetes/helm/ovn-kubernetes)...
✓ Helm install completed successfully (mode=dpu-host)
Applying external CRD manifests (ANP/BANP)...
✓ External CRDs applied successfully
Waiting for all pods in namespace: ovn-kubernetes to be ready...
✓ All Pods in namespace: ovn-kubernetes are ready
✓ OVN-Kubernetes pods are ready, installed via Helm successfully (mode=dpu-host)
✓ Deleted DaemonSet kube-system/kube-proxy
Creating DPU access secret for cross-cluster authentication...
Waiting for DPU access secret to be populated...
✓ DPU access secret created and populated

=== Installing addon multus on cluster dpu-sim-host ===
Patching Multus daemon config for OVN-Kubernetes (multusNamespace=default, clusterNetwork=ovn-primary)
✓ Created ovn-primary NetworkAttachmentDefinition
✓ Multus is installed
Waiting for pods in namespace: kube-system label: name=multus to be ready...
✓ Pods in namespace: kube-system label: name=multus are ready
✓ Scaled deployment kube-system/coredns to 0 (saved replicas in dpu-sim.io/suspend-replicas)

=== Patching cluster environment on dpu-sim-host ===
✓ Patched deployment kube-system/coredns for DPU-host simulated VF (dpusim.io/vf)
Deployment local-path-storage/local-path-provisioner not found, skipping DPU-host simulated VF patch
✓ Kubernetes cluster dpu-sim-host setup complete

=== Setting up Kubernetes cluster dpu-sim-dpu ===
Configuring OVS external_ids on DPU dpu-1-1 (encap-ip=192.168.123.22, host=host-1-1)...
✓ OVS external_ids configured on DPU dpu-1-1
Configuring OVS external_ids on DPU dpu-2-1 (encap-ip=192.168.123.23, host=host-2-1)...
✓ OVS external_ids configured on DPU dpu-2-1

=== Initializing first control plane node: master-2 ===
Initializing control plane on master-2 (ssh://root@192.168.120.24)...
K8s IP: 192.168.120.24 Pod CIDR: 10.246.0.0/16, Service CIDR: 10.247.0.0/16
Setting up kubectl on master-2 (ssh://root@192.168.120.24)...
✓ Control plane initialized on master-2
# ... (kubeadm worker join and control-plane join commands)
API server endpoint: https://192.168.120.24:6443
✓ Kubeconfig saved to: kubeconfig/dpu-sim-dpu.kubeconfig
=== Joining worker nodes ===
✓ Worker node joined to Kubernetes cluster: dpu-1-1
✓ Worker node joined to Kubernetes cluster: dpu-2-1

=== Aligning kubelet node-ip with k8s_node_ip (OVN underlay) ===
kubelet --node-ip set to 192.168.123.22 (ssh://root@192.168.120.94)
kubelet --node-ip set to 192.168.123.23 (ssh://root@192.168.120.85)
kubelet --node-ip set to 192.168.123.21 (ssh://root@192.168.120.24)

=== Installing flannel CNI on cluster dpu-sim-dpu ===
✓ Triggered rollout restart for daemonset kube-flannel/kube-flannel-ds
✓ Flannel is installed on cluster dpu-sim-dpu
Waiting for all pods in namespace: kube-flannel to be ready...
✓ All Pods in namespace: kube-flannel are ready

=== DPU offload enabled: auto-deploying OVN-Kubernetes in DPU mode on cluster dpu-sim-dpu ===
Installing OVN-Kubernetes (mode=dpu): Pod CIDR: 10.246.0.0/16, Service CIDR: 10.247.0.0/16, API Server: https://192.168.123.21:6443
Patching CoreDNS configmap for OVN-Kubernetes compatibility, dns server: 8.8.8.8
✓ CoreDNS configmap patched successfully
OVN-Kubernetes Helm image: 192.168.120.1:5000/ovn-kube:dpu-sim
Labeling nodes for single-node-zone interconnect...
✓ All nodes labeled for single-node-zone interconnect
✓ Master nodes labeled for OVN-Kubernetes HA
Labeling DPU nodes in cluster dpu-sim-dpu...
✓ DPU nodes labeled in cluster dpu-sim-dpu
Retrieving DPU host cluster credentials from kubeconfig/dpu-sim-host.kubeconfig...
✓ DPU host cluster credentials retrieved (API: https://192.168.123.11:6443, PodCIDR: 10.244.0.0/16/24, ServiceCIDR: 10.245.0.0/16)
Running Helm install for OVN-Kubernetes (mode=dpu, chart: /root/dpu-sim/ovn-kubernetes/helm/ovn-kubernetes)...
✓ Helm install completed successfully (mode=dpu)
Applying external CRD manifests (ANP/BANP)...
✓ External CRDs applied successfully
Waiting for all pods in namespace: ovn-kubernetes to be ready...
✓ All Pods in namespace: ovn-kubernetes are ready
✓ OVN-Kubernetes pods are ready, installed via Helm successfully (mode=dpu)

=== Installing addon multus on cluster dpu-sim-dpu ===
✓ Multus is installed
Waiting for pods in namespace: kube-system label: name=multus to be ready...
✓ Pods in namespace: kube-system label: name=multus are ready
✓ Scaled deployment kube-system/coredns to 0 (saved replicas in dpu-sim.io/suspend-replicas)
✓ Kubernetes cluster dpu-sim-dpu setup complete

=== Post-install (all clusters): restoring CoreDNS / local-path-provisioner and rolling out ===
Resuming system deployments on cluster dpu-sim-host
✓ Restored deployment kube-system/coredns to 2 replicas
failed to restart coredns: failed to update deployment kube-system/coredns: Operation cannot be fulfilled on deployments.apps "coredns": the object has been modified; please apply your changes to the latest version and try again
Resuming system deployments on cluster dpu-sim-dpu
✓ Restored deployment kube-system/coredns to 2 replicas
✓ Triggered rollout restart for deployment kube-system/coredns

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
  kubectl --kubeconfig kubeconfig/dpu-sim-host.kubeconfig get nodes
  kubectl --kubeconfig kubeconfig/dpu-sim-dpu.kubeconfig get nodes

Kubeconfig directory: kubeconfig
For more information, see README.md
dpu-sim total time: 12m59.067s
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

Same split host / DPU topology as Step 2a, using **`config-kind-ovnk-offload.yaml`** (Kind, `registry.enabled: false` loads OVN/device-plugin images into both clusters).

```bash
$ ./bin/dpu-sim --config=config-kind-ovnk-offload.yaml
╔═══════════════════════════════════════════════╗
║               DPU Simulator                   ║
╚═══════════════════════════════════════════════╝
Configuration: config-kind-ovnk-offload.yaml
Deployment mode: kind

=== Checking Dependencies ===
✓ Detected Linux distribution: rhel 9.6 (package manager: dnf, architecture: x86_64)
✓ wget is installed
# ... (pip3, jinjanator, git, openvswitch, kubectl, container runtime, kind)
✓ All dependencies are available

=== Cleaning up K8s ===
✓ Kubeconfig file removed: kubeconfig/dpu-sim-dpu.kubeconfig
✓ Kubeconfig file removed: kubeconfig/dpu-sim-host.kubeconfig

╔═══════════════════════════════════════════════╗
║      Kind-Based Deployment Workflow           ║
╚═══════════════════════════════════════════════╝

=== Cleaning up existing kind clusters ===
✓ Deleted Kind cluster: dpu-sim-host
✓ Deleted Kind cluster: dpu-sim-dpu

=== Ensuring Kind host prerequisites ===
✓ Inotify Limits is installed
✓ br_netfilter is installed
✓ All dependencies are available

=== Creating Kind Clusters ===
✓ Created Kind cluster: dpu-sim-host
✓ Kubeconfig saved to: kubeconfig/dpu-sim-host.kubeconfig
✓ Created Kind cluster: dpu-sim-dpu
✓ Kubeconfig saved to: kubeconfig/dpu-sim-dpu.kubeconfig

Cluster: dpu-sim-host
  Status: running
  Nodes:
    - dpu-sim-host-worker2 (worker) [NotReady]
    - dpu-sim-host-control-plane (control-plane) [NotReady]
    - dpu-sim-host-worker (worker) [NotReady]
# ... (IPv6, Open vSwitch, CNI plugins installed inside each node container)

Cluster: dpu-sim-dpu
  Status: running
  Nodes:
    - dpu-sim-dpu-worker (worker) [Unknown]
    - dpu-sim-dpu-control-plane (control-plane) [Unknown]
    - dpu-sim-dpu-worker2 (worker) [Unknown]
# ... (same per-node pattern)

Setting up veth topology for pair 0: dpu-sim-host-worker <-> dpu-sim-dpu-worker (16 data channels)
Assigned 10.89.0.254/24 to eth0-0 in dpu-sim-host-worker
Setting up veth topology for pair 1: dpu-sim-host-worker2 <-> dpu-sim-dpu-worker2 (16 data channels)
Assigned 10.89.0.253/24 to eth0-0 in dpu-sim-host-worker2
✓ Veth topology created for 2 host-DPU pairs (16 data channels each)

=== Installing CNI ===

=== Building registry container images (registry disabled; loading into Kind) ===
Using cached OVN-Kubernetes image ovn-kube:dpu-sim-a10d870f9d495f81
Loading image localhost/ovn-kube:dpu-sim into cluster dpu-sim-host (via podman save + kind load image-archive)...
# ... (layer copy / KIND_EXPERIMENTAL_PROVIDER lines)
✓ OVN-Kubernetes image loaded into cluster dpu-sim-host
Loading image localhost/ovn-kube:dpu-sim into cluster dpu-sim-dpu (via podman save + kind load image-archive)...
# ... (same for second cluster)
✓ OVN-Kubernetes image loaded into cluster dpu-sim-dpu
Building Device Plugin image dpu-sim-dp:latest (Architecture=x86_64)...
# ... (container build steps omitted)
✓ Device Plugin image built: dpu-sim-dp:latest
Loading image localhost/dpu-sim-dp:latest into cluster dpu-sim-host (via podman save + kind load image-archive)...
# ... (layer copy lines omitted)
✓ Device plugin image loaded into cluster dpu-sim-host
✓ Registry image builds complete; images loaded into Kind

=== Installing CNI on Kind clusters ===

--- Installing CNI on cluster dpu-sim-host ---
OVN-Kubernetes image was built and loaded into Kind (registry disabled; tag: ovn-kube:dpu-sim)
Internal API server IP for cluster dpu-sim-host: 10.89.0.61

=== Installing ovn-kubernetes CNI on cluster dpu-sim-host ===
Installing OVN-Kubernetes (mode=dpu-host): Pod CIDR: 10.244.0.0/16, Service CIDR: 10.245.0.0/16, API Server: https://10.89.0.61:6443
✓ CoreDNS configmap patched successfully
# ... (node labels, device plugin DaemonSet, Helm install, CRDs, wait for ovn-kubernetes pods)
✓ OVN-Kubernetes pods are ready, installed via Helm successfully (mode=dpu-host)
✓ DPU access secret created and populated

=== Installing addon multus on cluster dpu-sim-host ===
✓ Created ovn-primary NetworkAttachmentDefinition
✓ Multus is installed
# ... (CoreDNS / local-path scaled down for install)

=== Patching cluster environment on dpu-sim-host ===
✓ Patched deployment kube-system/coredns for DPU-host simulated VF (dpusim.io/vf)
✓ Patched deployment local-path-storage/local-path-provisioner for DPU-host simulated VF (dpusim.io/vf)

--- Installing CNI on cluster dpu-sim-dpu ---
Configuring OVS external_ids on DPU dpu-sim-dpu-worker (encap-ip=10.89.0.63, host=dpu-sim-host-worker)...
✓ OVS external_ids configured on DPU dpu-sim-dpu-worker
# ... (remaining DPU workers)
Internal API server IP for cluster dpu-sim-dpu: 10.89.0.64

=== Installing flannel CNI on cluster dpu-sim-dpu ===
✓ Flannel is installed on cluster dpu-sim-dpu
✓ All Pods in namespace: kube-flannel are ready

=== DPU offload enabled: auto-deploying OVN-Kubernetes in DPU mode on cluster dpu-sim-dpu ===
Installing OVN-Kubernetes (mode=dpu): Pod CIDR: 10.246.0.0/16, Service CIDR: 10.247.0.0/16, API Server: https://10.89.0.64:6443
✓ DPU host cluster credentials retrieved (API: https://10.89.0.61:6443, PodCIDR: 10.244.0.0/16/24, ServiceCIDR: 10.245.0.0/16)
# ... (Helm install dpu mode, CRDs, wait for ovn-kubernetes pods)
✓ OVN-Kubernetes pods are ready, installed via Helm successfully (mode=dpu)

=== Installing addon multus on cluster dpu-sim-dpu ===
✓ Multus is installed

=== Post-install (all clusters): restoring CoreDNS / local-path-provisioner and rolling out ===
# ... (restore replicas; occasional "object has been modified" on rollout — benign race)
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
  kubectl --kubeconfig kubeconfig/dpu-sim-host.kubeconfig get nodes
  kubectl --kubeconfig kubeconfig/dpu-sim-dpu.kubeconfig get nodes

Kubeconfig directory: kubeconfig
For more information, see README.md
dpu-sim total time: 6m8.833s
```

#### Post Deployment

After Installation finished, you should expect these software packages to be running:
- CRI-O container runtime
- `kubelet` (Kubernetes node agent)
- CNI & other kube-system pods are running

With VM OVN-Kubernetes DPU offload the host cluster looks like:

```bash
$ kubectl get nodes -o wide
NAME       STATUS   ROLES           AGE   VERSION    INTERNAL-IP      EXTERNAL-IP   OS-IMAGE                          KERNEL-VERSION           CONTAINER-RUNTIME
host-1-1   Ready    <none>          8h    v1.33.11   192.168.123.12   <none>        Fedora Linux 43 (Cloud Edition)   6.17.1-300.fc43.x86_64   cri-o://1.32.0
host-2-1   Ready    <none>          8h    v1.33.11   192.168.123.13   <none>        Fedora Linux 43 (Cloud Edition)   6.17.1-300.fc43.x86_64   cri-o://1.32.0
master-1   Ready    control-plane   8h    v1.33.11   192.168.123.11   <none>        Fedora Linux 43 (Cloud Edition)   6.17.1-300.fc43.x86_64   cri-o://1.32.0
```

```bash
$ kubectl get pods -A -o wide
NAMESPACE        NAME                                     READY   STATUS    RESTARTS   AGE     IP               NODE       NOMINATED NODE   READINESS GATES
kube-system      coredns-5f6c765946-5tpsn                 0/1     Running   0          7h57m   10.244.0.3       host-1-1   <none>           <none>
kube-system      coredns-5f6c765946-ckkpk                 0/1     Running   0          7h57m   10.244.1.3       host-2-1   <none>           <none>
kube-system      dpu-sim-device-plugin-ttfjx              1/1     Running   0          8h      192.168.123.13   host-2-1   <none>           <none>
kube-system      dpu-sim-device-plugin-xnrvz              1/1     Running   0          8h      192.168.123.12   host-1-1   <none>           <none>
kube-system      etcd-master-1                            1/1     Running   0          8h      192.168.120.51   master-1   <none>           <none>
kube-system      kube-apiserver-master-1                  1/1     Running   0          8h      192.168.120.51   master-1   <none>           <none>
kube-system      kube-controller-manager-master-1         1/1     Running   0          8h      192.168.120.51   master-1   <none>           <none>
kube-system      kube-multus-ds-b4dv9                     1/1     Running   0          8h      192.168.123.12   host-1-1   <none>           <none>
kube-system      kube-multus-ds-bjm2n                     1/1     Running   0          8h      192.168.123.11   master-1   <none>           <none>
kube-system      kube-multus-ds-ljg75                     1/1     Running   0          8h      192.168.123.13   host-2-1   <none>           <none>
kube-system      kube-scheduler-master-1                  1/1     Running   0          8h      192.168.120.51   master-1   <none>           <none>
ovn-kubernetes   ovnkube-control-plane-669fb74fd5-p2c5m   1/1     Running   0          8h      192.168.123.11   master-1   <none>           <none>
ovn-kubernetes   ovnkube-node-8kzgd                       6/6     Running   0          8h      192.168.123.11   master-1   <none>           <none>
ovn-kubernetes   ovnkube-node-dpu-host-dh46z              1/1     Running   0          8h      192.168.123.12   host-1-1   <none>           <none>
ovn-kubernetes   ovnkube-node-dpu-host-qbntq              1/1     Running   0          8h      192.168.123.13   host-2-1   <none>           <none>

```

The DPU cluster looks like this:

```bash
$ kubectl get nodes -o wide
NAME       STATUS   ROLES           AGE   VERSION    INTERNAL-IP      EXTERNAL-IP   OS-IMAGE                          KERNEL-VERSION           CONTAINER-RUNTIME
dpu-1-1    Ready    <none>          8h    v1.33.11   192.168.123.22   <none>        Fedora Linux 43 (Cloud Edition)   6.17.1-300.fc43.x86_64   cri-o://1.32.0
dpu-2-1    Ready    <none>          8h    v1.33.11   192.168.123.23   <none>        Fedora Linux 43 (Cloud Edition)   6.17.1-300.fc43.x86_64   cri-o://1.32.0
master-2   Ready    control-plane   8h    v1.33.11   192.168.123.21   <none>        Fedora Linux 43 (Cloud Edition)   6.17.1-300.fc43.x86_64   cri-o://1.32.0
```

```bash
$ kubectl get pods -A -o wide
NAMESPACE        NAME                               READY   STATUS    RESTARTS   AGE     IP               NODE       NOMINATED NODE   READINESS GATES
kube-flannel     kube-flannel-ds-6fzxg              1/1     Running   0          8h      192.168.123.22   dpu-1-1    <none>           <none>
kube-flannel     kube-flannel-ds-h76rm              1/1     Running   0          8h      192.168.123.23   dpu-2-1    <none>           <none>
kube-flannel     kube-flannel-ds-mgrjx              1/1     Running   0          8h      192.168.123.21   master-2   <none>           <none>
kube-system      coredns-5d6775bdfc-hxkcd           1/1     Running   0          7h59m   10.246.2.2       dpu-2-1    <none>           <none>
kube-system      coredns-5d6775bdfc-rsmx9           1/1     Running   0          7h59m   10.246.1.3       dpu-1-1    <none>           <none>
kube-system      etcd-master-2                      1/1     Running   0          8h      192.168.120.24   master-2   <none>           <none>
kube-system      kube-apiserver-master-2            1/1     Running   0          8h      192.168.120.24   master-2   <none>           <none>
kube-system      kube-controller-manager-master-2   1/1     Running   0          8h      192.168.120.24   master-2   <none>           <none>
kube-system      kube-multus-ds-cqcpz               1/1     Running   0          7h59m   192.168.123.21   master-2   <none>           <none>
kube-system      kube-multus-ds-jn9sw               1/1     Running   0          7h59m   192.168.123.22   dpu-1-1    <none>           <none>
kube-system      kube-multus-ds-zwrz7               1/1     Running   0          7h59m   192.168.123.23   dpu-2-1    <none>           <none>
kube-system      kube-proxy-jgb46                   1/1     Running   0          8h      192.168.123.21   master-2   <none>           <none>
kube-system      kube-proxy-sgsrs                   1/1     Running   0          8h      192.168.123.23   dpu-2-1    <none>           <none>
kube-system      kube-proxy-wcm29                   1/1     Running   0          8h      192.168.123.22   dpu-1-1    <none>           <none>
kube-system      kube-scheduler-master-2            1/1     Running   0          8h      192.168.120.24   master-2   <none>           <none>
ovn-kubernetes   ovnkube-node-dpu-pvh8h             6/6     Running   0          8h      192.168.123.23   dpu-2-1    <none>           <none>
ovn-kubernetes   ovnkube-node-dpu-x55bp             6/6     Running   0          8h      192.168.123.22   dpu-1-1    <none>           <none>
```

With Kind OVN-Kubernetes DPU offload the host cluster looks like:

```bash
$ kubectl get nodes -o wide
NAME                         STATUS   ROLES           AGE   VERSION   INTERNAL-IP   EXTERNAL-IP   OS-IMAGE                         KERNEL-VERSION                 CONTAINER-RUNTIME
dpu-sim-host-control-plane   Ready    control-plane   8h    v1.35.0   10.89.0.61    <none>        Debian GNU/Linux 12 (bookworm)   5.14.0-570.71.1.el9_6.x86_64   containerd://2.2.0
dpu-sim-host-worker          Ready    <none>          8h    v1.35.0   10.89.0.62    <none>        Debian GNU/Linux 12 (bookworm)   5.14.0-570.71.1.el9_6.x86_64   containerd://2.2.0
dpu-sim-host-worker2         Ready    <none>          8h    v1.35.0   10.89.0.60    <none>        Debian GNU/Linux 12 (bookworm)   5.14.0-570.71.1.el9_6.x86_64   containerd://2.2.0
```

```bash
$ kubectl  get pods -A -o wide
NAMESPACE            NAME                                                 READY   STATUS    RESTARTS     AGE   IP           NODE                         NOMINATED NODE   READINESS GATES
kube-system          coredns-56d46477b-nsw84                              1/1     Running   0            8h    10.244.2.3   dpu-sim-host-worker          <none>           <none>
kube-system          coredns-56d46477b-sdlp5                              1/1     Running   0            8h    10.244.1.6   dpu-sim-host-worker2         <none>           <none>
kube-system          dpu-sim-device-plugin-2l2nd                          1/1     Running   0            8h    10.89.0.62   dpu-sim-host-worker          <none>           <none>
kube-system          dpu-sim-device-plugin-z9nsz                          1/1     Running   0            8h    10.89.0.60   dpu-sim-host-worker2         <none>           <none>
kube-system          etcd-dpu-sim-host-control-plane                      1/1     Running   0            8h    10.89.0.61   dpu-sim-host-control-plane   <none>           <none>
kube-system          kube-apiserver-dpu-sim-host-control-plane            1/1     Running   0            8h    10.89.0.61   dpu-sim-host-control-plane   <none>           <none>
kube-system          kube-controller-manager-dpu-sim-host-control-plane   1/1     Running   0            8h    10.89.0.61   dpu-sim-host-control-plane   <none>           <none>
kube-system          kube-multus-ds-52g7m                                 1/1     Running   2 (8h ago)   8h    10.89.0.60   dpu-sim-host-worker2         <none>           <none>
kube-system          kube-multus-ds-9x4rk                                 1/1     Running   0            8h    10.89.0.62   dpu-sim-host-worker          <none>           <none>
kube-system          kube-multus-ds-znvrp                                 1/1     Running   0            8h    10.89.0.61   dpu-sim-host-control-plane   <none>           <none>
kube-system          kube-scheduler-dpu-sim-host-control-plane            1/1     Running   0            8h    10.89.0.61   dpu-sim-host-control-plane   <none>           <none>
local-path-storage   local-path-provisioner-5cb576d97c-hk45w              1/1     Running   0            8h    10.244.1.8   dpu-sim-host-worker2         <none>           <none>
ovn-kubernetes       ovnkube-control-plane-bc889f977-22jgp                1/1     Running   0            8h    10.89.0.61   dpu-sim-host-control-plane   <none>           <none>
ovn-kubernetes       ovnkube-node-dpu-host-52x2s                          1/1     Running   0            8h    10.89.0.62   dpu-sim-host-worker          <none>           <none>
ovn-kubernetes       ovnkube-node-dpu-host-kqd6f                          1/1     Running   0            8h    10.89.0.60   dpu-sim-host-worker2         <none>           <none>
ovn-kubernetes       ovnkube-node-ntgwg                                   6/6     Running   0            8h    10.89.0.61   dpu-sim-host-control-plane   <none>           <none>
```

The DPU cluster looks like this:

```bash
$ kubectl get nodes -o wide
NAME                        STATUS   ROLES           AGE   VERSION   INTERNAL-IP   EXTERNAL-IP   OS-IMAGE                         KERNEL-VERSION                 CONTAINER-RUNTIME
dpu-sim-dpu-control-plane   Ready    control-plane   8h    v1.35.0   10.89.0.64    <none>        Debian GNU/Linux 12 (bookworm)   5.14.0-570.71.1.el9_6.x86_64   containerd://2.2.0
dpu-sim-dpu-worker          Ready    <none>          8h    v1.35.0   10.89.0.63    <none>        Debian GNU/Linux 12 (bookworm)   5.14.0-570.71.1.el9_6.x86_64   containerd://2.2.0
dpu-sim-dpu-worker2         Ready    <none>          8h    v1.35.0   10.89.0.65    <none>        Debian GNU/Linux 12 (bookworm)   5.14.0-570.71.1.el9_6.x86_64   containerd://2.2.0
```

```bash
$ kubectl  get pods -A -o wide
NAMESPACE            NAME                                                READY   STATUS    RESTARTS   AGE   IP           NODE                        NOMINATED NODE   READINESS GATES
kube-flannel         kube-flannel-ds-5nclf                               1/1     Running   0          8h    10.89.0.64   dpu-sim-dpu-control-plane   <none>           <none>
kube-flannel         kube-flannel-ds-657nl                               1/1     Running   0          8h    10.89.0.63   dpu-sim-dpu-worker          <none>           <none>
kube-flannel         kube-flannel-ds-mpb4g                               1/1     Running   0          8h    10.89.0.65   dpu-sim-dpu-worker2         <none>           <none>
kube-system          coredns-7d764666f9-d275m                            1/1     Running   0          8h    10.246.0.2   dpu-sim-dpu-control-plane   <none>           <none>
kube-system          coredns-7d764666f9-x9dpt                            1/1     Running   0          8h    10.246.2.2   dpu-sim-dpu-worker2         <none>           <none>
kube-system          etcd-dpu-sim-dpu-control-plane                      1/1     Running   0          8h    10.89.0.64   dpu-sim-dpu-control-plane   <none>           <none>
kube-system          kube-apiserver-dpu-sim-dpu-control-plane            1/1     Running   0          8h    10.89.0.64   dpu-sim-dpu-control-plane   <none>           <none>
kube-system          kube-controller-manager-dpu-sim-dpu-control-plane   1/1     Running   0          8h    10.89.0.64   dpu-sim-dpu-control-plane   <none>           <none>
kube-system          kube-multus-ds-5b7jz                                1/1     Running   0          8h    10.89.0.65   dpu-sim-dpu-worker2         <none>           <none>
kube-system          kube-multus-ds-lwpvg                                1/1     Running   0          8h    10.89.0.64   dpu-sim-dpu-control-plane   <none>           <none>
kube-system          kube-multus-ds-zjncd                                1/1     Running   0          8h    10.89.0.63   dpu-sim-dpu-worker          <none>           <none>
kube-system          kube-proxy-5jjs7                                    1/1     Running   0          8h    10.89.0.65   dpu-sim-dpu-worker2         <none>           <none>
kube-system          kube-proxy-d9fx5                                    1/1     Running   0          8h    10.89.0.64   dpu-sim-dpu-control-plane   <none>           <none>
kube-system          kube-proxy-mv9gp                                    1/1     Running   0          8h    10.89.0.63   dpu-sim-dpu-worker          <none>           <none>
kube-system          kube-scheduler-dpu-sim-dpu-control-plane            1/1     Running   0          8h    10.89.0.64   dpu-sim-dpu-control-plane   <none>           <none>
local-path-storage   local-path-provisioner-67b8995b4b-2vt27             1/1     Running   0          8h    10.246.2.3   dpu-sim-dpu-worker2         <none>           <none>
ovn-kubernetes       ovnkube-node-dpu-7n8tv                              6/6     Running   0          8h    10.89.0.65   dpu-sim-dpu-worker2         <none>           <none>
ovn-kubernetes       ovnkube-node-dpu-7q27r                              6/6     Running   0          8h    10.89.0.63   dpu-sim-dpu-worker          <none>           <none>
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

### Cannot connect to VMs via SSH

1. Verify VM is running: `./bin/vmctl list`
2. Check VM has IP address
3. Try SSH access: `./bin/vmctl ssh host-1`
4. Verify SSH key exists: `ls -la ~/.ssh/id_rsa*`

### Permission denied errors with VM deployment

Make sure your user is in the `libvirt` group:
```bash
groups | grep libvirt
```

If not, add yourself and log out/in:
```bash
sudo usermod -a -G libvirt $USER
```

### Cannot download cloud image for VMs

The download may take time depending on your connection. If it fails:
1. Check internet connectivity
2. Verify the image URL in `config.yaml` is correct
3. Manually download to `/var/lib/libvirt/images/`

### View Cluster Logs for VMs

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
                                         ┌──────────────────┐
                                         │ Optional: local  │
                                         │ registry setup   │
                                         │ (if enabled: CNI │
                                         │  images, device  │
                                         │  plugin)         │
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
           ┌──────────────────────────────────┐   │            ┌────────────────────────┐
           │ EnsureHostNetworkPrerequisites() │   │            │   NewKindManager()     │
           │ (libvirt / OVS host)             │   │            │   (Kind provider)      │
           └───────────┬──────────────────────┘   │            └───────────┬────────────┘
                       ▼                          │                        │
           ┌────────────────────────┐             │                        ▼
           │    NewVMManager()      │             │            ┌────────────────────────┐
           │  (connect to libvirt)  │             │            │    CleanupAll()        │
           └───────────┬────────────┘             │            │  (Delete Kind clusters)│
                       │                          │            └───────────┬────────────┘
                       ▼                          │                        │
           ┌────────────────────────┐             │                        │
           │    CleanupAll()        │             │                        │
           │  (VMs, Networks, Disks)│             │                        │
           └───────────┬────────────┘             │                        │
                       │                          │                        │
═══════════════════════╪══════════════════════════╪════════════════════════╪═══════════════════════════════════════
                       │  PHASE 1: INFRASTRUCTURE │                        │  PHASE 1: CLUSTER CREATION
═══════════════════════╪══════════════════════════╪════════════════════════╪═══════════════════════════════════════
                       ▼                          │                        ▼
           ┌────────────────────────┐             │            ┌──────────────────────────┐
           │  CreateAllNetworks()   │             │            │ InstallHostDeps (Kind)   │
           └───────────┬────────────┘             │            │ (inotify, br_netfilter)  │
                       │                          │            │ (if Flannel / Multus)    │
                       │                          │            └───────────┬──────────────┘
                       │                          │                        ▼
                       │                          │            ┌────────────────────────┐
                       │                          │            │  DeployAllClusters()   │
                       │                          │            └───────────┬────────────┘
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
           └───────────┬────────────┘             │            ┌────────────────────────────────┐
                       │                          │            │   GetKubeconfig()              │
                       │ (for each VM)            │            │   Save to file                 │
                       │                          │            │ (+ add img registry Kind net)  │
                       ▼                          │            └───────────┬────────────────────┘
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
                       ▼                          │                        ▼
           ┌────────────────────────┐             │            ┌────────────────────────┐
           │ DomainDefineXML()      │             │            │ SetupHostToDpuNetwork()│
           │ SetAutostart()         │             │            │ (HostToDpu cfg, veth   │
           │ domain.Create()        │◀─── Start  │            │  host...DPU containers)│
           └───────────┬────────────┘     QEMU    │            └───────────┬────────────┘
                       │                          │                        │
                       ▼                          │                        │
           ┌────────────────────────┐             │                        │
           │   WaitForVMIP()        │             │                        │
           │   (DHCP lease, 5min)   │             │                        │
           │   WaitForSSH()         │             │                        │
           │   (SSH ready, 5min)    │             │                        │
           │   cloud-init status    │             │                        │
           │     --wait (per VM)    │             │                        │
           └───────────┬────────────┘             │                        │
                       ▼                          │                        │
           ┌────────────────────────┐             │                        │
           │ AssignDpuHostGatewayIPs│             │                        │
           │ (if offload DPU Host:  │             │                        │
           │  gw on eth0-0, etc.)   │             │                        │
           └───────────┬────────────┘             │                        │
                       │                          │                        │
═══════════════════════╪══════════════════════════╪════════════════════════╪═══════════════════════════════════════
                       │  PHASE 2: K8s + CNI      │                        │  PHASE 2: K8s + CNI
═══════════════════════╪══════════════════════════╪════════════════════════╪═══════════════════════════════════════
                       ▼                          │                        ▼
           ┌────────────────────────┐             │            ┌────────────────────────┐
           │ InstallKubernetes()    │             │            │ (Optional) Build/load  │
           │ (on each VM via SSH)   │             │            │ registry-only images   │
           │ - containerd           │             │            │ into Kind (see config) │
           │ - kubeadm, kubelet     │             │            └───────────┬────────────┘
           │ - kubectl              │             │                        │
           └───────────┬────────────┘             │                        ▼
                       │                          │            ┌────────────────────────┐
                       ▼                          │            │    InstallCNI()        │
           ┌───────────────────────────┐          │            │ (per cluster, ordered) │
           │ SetupAllK8sClusters()     │          │            └───────────┬────────────┘
           │ • kubeadm init + save     │          │                        │
           │   kubeconfig (1st CP)     │          │                        │
           │ • join CP / workers       │          │               ┌────────┴────────┐
           │ • kubelet --node-ip from  │          │               ▼                 ▼
           │   k8s_node_ip (OVN)       │          │     ┌─────────────────┐  ┌─────────────────┐
           │ • DPU OVS prep if req.    │          │     │ OVN-Kubernetes? │  │ Other CNI       │
           └───────────┬───────────────┘          │     │ (+ DPU offload) │  │ (Flannel, etc.) │
                       │                          │     └────────┬────────┘  └────────┬────────┘
               ┌───────┴────────┐                 │              │                    │
               ▼                ▼                 │              ▼                    │
     ┌─────────────────┐ ┌─────────────────┐      │     ┌─────────────────┐           │
     │ OVN-Kubernetes? │ │ Other CNI       │      │     │PullAndLoadImage │           │
     │ (+ DPU offload) │ │ (Flannel, etc.) │      │     │+ DPU OVS cfg in │           │
     └────────┬────────┘ └──────┬──────────┘      │     │ Kind if offload │           │
              │                 │                 │     └────────┬────────┘           │
              └────────┬────────┘                 │              │                    │
                       ▼                          │              └─────────┬──────────┘
           ┌────────────────────────┐             │                        ▼
           │ cniMgr.InstallCNI()    │             │            ┌────────────────────────┐
           │ (Helm from host; cfg)  │             │            │ GetInternalAPIServerIP │
           │  from cluster CNI type │             │            │ (docker inspect)       │
           └───────────┬────────────┘             │            └───────────┬────────────┘
                       │                          │                        ▼
                       ▼                          │            ┌────────────────────────┐
           ┌────────────────────────┐             │            │ cniMgr.InstallCNI()    │
           │ InstallAddons()        │             │            │ (same Helm path as VM) │
           │ PostInstallPerCluster()│             │            └───────────┬────────────┘
           │ cni.PostInstall()      │             │                        ▼
           │ (after all clusters)   │             │            ┌────────────────────────┐
           └───────────┬────────────┘             │            │ InstallAddons()        │
                       │                          │            │ PostInstallPerCluster()│
                       │                          │            │ cni.PostInstall()      │
                       │                          │            └───────────┬────────────┘
                       │                          │                        │
═══════════════════════╪══════════════════════════╪════════════════════════╪═══════════════════════════════════════
                       │                          │                        │
                       ▼                          │                        ▼
           ┌────────────────────────┐             │            ┌────────────────────────┐
           │  VM Deployment         │             │            │  Kind Deployment       │
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
