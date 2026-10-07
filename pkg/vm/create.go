package vm

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/network"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

// CreateVM creates a complete VM including disk, cloud-init ISO, and domain
func (m *VMManager) CreateVM(vmCfg config.VMConfig) error {
	log.Info("=== Creating VM: %s ===", vmCfg.Name)

	if m.VMExists(vmCfg.Name) {
		return fmt.Errorf("VM %s already exists", vmCfg.Name)
	}

	imagePath := GetImagePath(m.config.OperatingSystem)

	if err := EnsureCloudImage(m.hostExec, m.config.OperatingSystem, imagePath); err != nil {
		return fmt.Errorf("failed to ensure cloud image: %w", err)
	}

	diskPath, err := CreateVMDisk(m.hostExec, vmCfg.Name, vmCfg.DiskSize, imagePath)
	if err != nil {
		return fmt.Errorf("failed to create VM disk: %w", err)
	}

	cloudInitPath, err := CreateCloudInitISO(m.hostExec, m.config.SSH, vmCfg, m.config)
	if err != nil {
		return fmt.Errorf("failed to create cloud-init ISO: %w", err)
	}

	spec := m.hostSpec
	var nvramPath string
	if m.hostDistro.Architecture == platform.AARCH64 && (spec.uefiLoader == "" || spec.uefiVarsTemplate == "") {
		return fmt.Errorf("missing aarch64 UEFI firmware: install edk2/aavmf and ensure QEMU_EFI-pflash and vars template are available")
	}
	if spec.uefiLoader != "" && spec.uefiVarsTemplate != "" {
		nvramPath, err = ensureUEFINvram(vmCfg.Name, spec.uefiVarsTemplate)
		if err != nil {
			return fmt.Errorf("failed to prepare UEFI NVRAM: %w", err)
		}
	}

	// Generate libvirt domain XML
	xml := m.GenerateVMXML(vmCfg, diskPath, cloudInitPath, spec, nvramPath)

	domain, err := m.conn.DomainDefineXML(xml)
	if err != nil {
		return fmt.Errorf("failed to define domain: %w", err)
	}
	defer domain.Free()

	if err := domain.SetAutostart(true); err != nil {
		return fmt.Errorf("failed to set autostart: %w", err)
	}

	if err := domain.Create(); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	log.Info("✓ Created and started VM: %s", vmCfg.Name)
	return nil
}

func (m *VMManager) generateNetworkInterfaces(vmCfg config.VMConfig) string {
	var sb strings.Builder

	for _, network := range m.config.Networks {
		// AttachTo determines which type of VM the network should be attached to.
		if network.AttachTo != "any" && network.AttachTo != vmCfg.Type {
			continue
		}
		if network.Type == config.GatewayNetworkName {
			if vmCfg.Type != config.DpuType || vmCfg.Host == "" {
				continue
			}
		}
		mac := GenerateMACForNetwork(vmCfg.Name, network.Type)
		if network.Type == config.K8sNetworkName && vmCfg.K8sNodeMAC != "" {
			mac = vmCfg.K8sNodeMAC
		}
		sb.WriteString("    <interface type='network'>\n")
		sb.WriteString(fmt.Sprintf("      <mac address='%s'/>\n", mac))
		sb.WriteString(fmt.Sprintf("      <source network='%s'/>\n", network.Name))
		if network.UseOVS {
			sb.WriteString("      <virtualport type='openvswitch'/>\n")
		}
		sb.WriteString(fmt.Sprintf("      <model type='%s'/>\n", network.NICModel))
		sb.WriteString("    </interface>\n")
	}

	return sb.String()
}

type archSpec struct {
	// libvirtArch/machine/cpuMode/emulator define the domain's compute platform.
	libvirtArch string
	machine     string
	cpuMode     string
	emulator    string
	// uefiLoader/uefiVarsTemplate are optional and set when firmware-backed boot
	// is available on the host architecture.
	uefiLoader       string
	uefiVarsTemplate string
	// Feature flags map directly to optional XML blocks for device/interrupt
	// behavior across architectures.
	enableIOMMU bool
	enableAPIC  bool
	enableACPI  bool
}

func hostArchSpec(hostArch platform.Architecture) (archSpec, error) {
	emulator, err := resolveQEMUEmulator(hostArch)
	if err != nil {
		return archSpec{}, err
	}

	switch hostArch {
	case platform.AARCH64:
		loader, varsTemplate := findAarch64UEFIFirmware()
		return archSpec{
			libvirtArch:      "aarch64",
			machine:          "virt",
			cpuMode:          "host-passthrough",
			emulator:         emulator,
			uefiLoader:       loader,
			uefiVarsTemplate: varsTemplate,
			enableIOMMU:      false,
			enableAPIC:       false,
			enableACPI:       loader != "",
		}, nil

	case platform.X86_64:
		return archSpec{
			libvirtArch: "x86_64",
			machine:     "q35",
			cpuMode:     "host-passthrough",
			emulator:    emulator,
			enableIOMMU: true,
			enableAPIC:  true,
			enableACPI:  true,
		}, nil
	default:
		return archSpec{}, fmt.Errorf("unsupported Architecture: %v", hostArch)
	}
}

// resolveQEMUEmulator matches the hosts architecture to its proper qemu binary
// and calls resolveFirstAvailableEmulator to search for one of those binaries and returns it
// Why: It's useful because depending on the distro (non-rhel vs rhel) the binary is path and name varies
func resolveQEMUEmulator(hostArch platform.Architecture) (string, error) {
	var candidates []string
	switch hostArch {
	case platform.X86_64:
		candidates = []string{
			"/usr/libexec/qemu-kvm",
			"/usr/bin/qemu-kvm",
			"/usr/bin/qemu-system-x86_64",
			"qemu-kvm",
			"qemu-system-x86_64",
		}
	case platform.AARCH64:
		candidates = []string{
			"/usr/bin/qemu-system-aarch64",
			"qemu-system-aarch64",
		}
	default:
		return "", fmt.Errorf("unsupported Architecture: %v", hostArch)
	}

	emulator, ok := resolveFirstAvailableEmulator(candidates, fileExists, exec.LookPath)
	if !ok {
		return "", fmt.Errorf("no suitable QEMU emulator found for host architecture %s (checked: %s)", hostArch, strings.Join(candidates, ", "))
	}

	return emulator, nil
}

// resolveFirstAvailableEmulator returns the first emulator candidate that
// exists on the host.
//
// Why: qemu binary names and install paths vary across distros and packaging
// layouts, so caller-provided candidates include both absolute paths and PATH
// lookups.
//
// How: absolute candidates are validated with existsFn, while non-absolute
// candidates are resolved with lookPathFn; the first match wins.
func resolveFirstAvailableEmulator(candidates []string, existsFn func(string) bool, lookPathFn func(string) (string, error)) (string, bool) {
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate, "/") {
			if existsFn(candidate) {
				return candidate, true
			}
			continue
		}

		resolved, err := lookPathFn(candidate)
		if err == nil && resolved != "" {
			return resolved, true
		}
	}
	return "", false
}

type firmwareCandidate struct {
	loader string
	vars   string
}

func findAarch64UEFIFirmware() (string, string) {
	// Common Fedora/edk2 locations
	candidates := []firmwareCandidate{
		{"/usr/share/AAVMF/AAVMF_CODE.fd", "/usr/share/AAVMF/AAVMF_VARS.fd"},
		{"/usr/share/edk2/aarch64/QEMU_EFI-pflash.raw", "/usr/share/edk2/aarch64/vars-template-pflash.raw"},
		{"/usr/share/edk2/aarch64/QEMU_EFI-qemuvars-pflash.raw", "/usr/share/edk2/aarch64/vars-template-pflash.raw"},
		{"/usr/share/edk2/aarch64/QEMU_EFI-pflash.qcow2", "/usr/share/edk2/aarch64/vars-template-pflash.qcow2"},
		{"/usr/share/edk2/aarch64/QEMU_EFI-qemuvars-pflash.qcow2", "/usr/share/edk2/aarch64/vars-template-pflash.qcow2"},
		{"/usr/share/edk2/aarch64/QEMU_EFI.fd", "/usr/share/edk2/aarch64/QEMU_VARS.fd"},
		{"/usr/share/edk2/aarch64/edk2-aarch64-code.fd", "/usr/share/edk2/aarch64/edk2-aarch64-vars.fd"},
		{"/usr/share/edk2/aarch64/edk2-arm-code.fd", "/usr/share/edk2/aarch64/edk2-arm-vars.fd"},
	}
	return findAarch64UEFIFirmwareWithStat(candidates, os.Stat)
}

func findAarch64UEFIFirmwareWithStat(candidates []firmwareCandidate, statFn func(string) (os.FileInfo, error)) (string, string) {
	const minPflashSize = 64 * 1024 * 1024
	for _, c := range candidates {
		loaderInfo, err := statFn(c.loader)
		if err != nil || loaderInfo.IsDir() {
			continue
		}
		varsInfo, err := statFn(c.vars)
		if err != nil || varsInfo.IsDir() {
			continue
		}
		if loaderInfo.Size() >= minPflashSize && varsInfo.Size() >= minPflashSize {
			return c.loader, c.vars
		}
	}
	return "", ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func nvramPathForTemplate(vmName, templatePath string) string {
	ext := filepath.Ext(templatePath)
	if ext == "" {
		ext = ".fd"
	}
	return filepath.Join("/var/lib/libvirt/qemu/nvram", fmt.Sprintf("%s_VARS%s", vmName, ext))
}

func ensureUEFINvram(vmName, templatePath string) (string, error) {
	nvramPath := nvramPathForTemplate(vmName, templatePath)
	if fileExists(nvramPath) {
		return nvramPath, nil
	}
	if err := os.MkdirAll(filepath.Dir(nvramPath), 0o755); err != nil {
		return "", err
	}
	src, err := os.Open(templatePath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := os.OpenFile(nvramPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return "", err
	}
	return nvramPath, nil
}

// GenerateVMXML generates libvirt domain XML for a VM
func (m *VMManager) GenerateVMXML(vmCfg config.VMConfig, diskPath, cloudInitPath string, spec archSpec, nvramPath string) string {
	var sb strings.Builder

	sb.WriteString("<domain type='kvm'>\n")
	sb.WriteString(fmt.Sprintf("  <name>%s</name>\n", vmCfg.Name))
	sb.WriteString(fmt.Sprintf("  <memory unit='MiB'>%d</memory>\n", vmCfg.Memory))
	sb.WriteString(fmt.Sprintf("  <vcpu>%d</vcpu>\n", vmCfg.VCPUs))

	sb.WriteString("  <os>\n")
	sb.WriteString(fmt.Sprintf("    <type arch='%s' machine='%s'>hvm</type>\n", spec.libvirtArch, spec.machine))
	if spec.uefiLoader != "" {
		sb.WriteString(fmt.Sprintf("    <loader readonly='yes' type='pflash'>%s</loader>\n", spec.uefiLoader))
		if nvramPath != "" {
			sb.WriteString(fmt.Sprintf("    <nvram>%s</nvram>\n", nvramPath))
		}
	}
	sb.WriteString("    <boot dev='hd'/>\n")
	sb.WriteString("  </os>\n")

	sb.WriteString("  <features>\n")
	if spec.enableACPI {
		sb.WriteString("    <acpi/>\n")
	}
	if spec.enableAPIC {
		sb.WriteString("    <apic/>\n")
		sb.WriteString("    <ioapic driver='qemu'/>\n")
	}
	sb.WriteString("  </features>\n")

	if spec.cpuMode != "" {
		sb.WriteString(fmt.Sprintf("  <cpu mode='%s'/>\n", spec.cpuMode))
	}

	if spec.enableIOMMU {
		sb.WriteString("  <iommu model='intel'>\n")
		sb.WriteString("    <driver intremap='on' caching_mode='on' iotlb='on'/>\n")
		sb.WriteString("  </iommu>\n")
	}

	sb.WriteString("  <clock offset='utc'/>\n")

	sb.WriteString("  <on_poweroff>destroy</on_poweroff>\n")
	sb.WriteString("  <on_reboot>restart</on_reboot>\n")
	sb.WriteString("  <on_crash>destroy</on_crash>\n")

	sb.WriteString("  <devices>\n")
	sb.WriteString(fmt.Sprintf("    <emulator>%s</emulator>\n", spec.emulator))

	sb.WriteString("    <disk type='file' device='disk'>\n")
	sb.WriteString("      <driver name='qemu' type='qcow2'/>\n")
	sb.WriteString(fmt.Sprintf("      <source file='%s'/>\n", diskPath))
	sb.WriteString("      <target dev='vda' bus='virtio'/>\n")
	sb.WriteString("    </disk>\n")

	sb.WriteString("    <disk type='file' device='cdrom'>\n")
	sb.WriteString("      <driver name='qemu' type='raw'/>\n")
	sb.WriteString(fmt.Sprintf("      <source file='%s'/>\n", cloudInitPath))
	sb.WriteString("      <target dev='sda' bus='sata'/>\n")
	sb.WriteString("      <readonly/>\n")
	sb.WriteString("    </disk>\n")

	// Generate network interfaces from looking at the Networks configuration section and seeing if the
	// VM is attached to any of the networks. If so, generate the network interface XML for each of those
	// networks.
	sb.WriteString(m.generateNetworkInterfaces(vmCfg))

	numPairs := m.config.GetHostToDpuNumPairs()
	mappings := m.config.GetHostDPUMappings()
	hostToDpuNic := "virtio"
	if net := m.config.GetHostToDpuNetwork(); net != nil && net.NICModel != "" {
		hostToDpuNic = net.NICModel
	}

	// Add host-to-DPU network interfaces. Deterministic MAC per (vm, index); udev in guest renames by MAC.
	if vmCfg.Type == config.HostType {
		for _, mapping := range mappings {
			if mapping.Host.Name == vmCfg.Name {
				for _, conn := range mapping.Connections {
					for idx := 0; idx < numPairs; idx++ {
						netName := network.GetHostToDPUNetworkName(mapping.Host.Name, conn.DPU.Name, idx)
						mac := network.GenerateMACForHostToDpu(vmCfg.Name, config.HostType, idx)
						sb.WriteString("    <interface type='network'>\n")
						sb.WriteString(fmt.Sprintf("      <mac address='%s'/>\n", mac))
						sb.WriteString(fmt.Sprintf("      <source network='%s'/>\n", netName))
						sb.WriteString("      <virtualport type='openvswitch'/>\n")
						sb.WriteString(fmt.Sprintf("      <model type='%s'/>\n", hostToDpuNic))
						sb.WriteString("    </interface>\n")
					}
				}
				break
			}
		}
	}

	if vmCfg.Type == config.DpuType {
		for _, mapping := range mappings {
			for _, conn := range mapping.Connections {
				if conn.DPU.Name == vmCfg.Name {
					for idx := 0; idx < numPairs; idx++ {
						netName := network.GetHostToDPUNetworkName(mapping.Host.Name, conn.DPU.Name, idx)
						mac := network.GenerateMACForHostToDpu(vmCfg.Name, config.DpuType, idx)
						sb.WriteString("    <interface type='network'>\n")
						sb.WriteString(fmt.Sprintf("      <mac address='%s'/>\n", mac))
						sb.WriteString(fmt.Sprintf("      <source network='%s'/>\n", netName))
						sb.WriteString("      <virtualport type='openvswitch'/>\n")
						sb.WriteString(fmt.Sprintf("      <model type='%s'/>\n", hostToDpuNic))
						sb.WriteString("    </interface>\n")
					}
					break
				}
			}
		}
	}

	sb.WriteString("    <console type='pty'>\n")
	sb.WriteString("      <target type='serial' port='0'/>\n")
	sb.WriteString("    </console>\n")

	sb.WriteString("    <graphics type='vnc' port='-1' autoport='yes'/>\n")

	sb.WriteString("  </devices>\n")
	sb.WriteString("</domain>\n")

	return sb.String()
}

// CreateAllVMs creates all VMs defined in the configuration
func (m *VMManager) CreateAllVMs() error {
	log.Info("=== Creating All VMs ===")

	for _, vmCfg := range m.config.VMs {
		if err := m.CreateVM(vmCfg); err != nil {
			return fmt.Errorf("failed to create VM %s: %w", vmCfg.Name, err)
		}
	}

	log.Info("✓ All VMs created successfully")
	return nil
}
