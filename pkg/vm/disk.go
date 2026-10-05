package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/network"
	oras "oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const (
	// Default image directory
	DefaultImageDir = "/var/lib/libvirt/images"
)

var pullCloudImageFromOCI = pullOCICloudImage

// TODO: Add documentation on how to build and publish OCI artifacts using oras
// A relevant gh actions workflow can be seen here: https://github.com/SamD2021/dpu-image-factory/blob/a0ebfd28991586594bfd24bd1d851e73e05a7960/.github/workflows/build.yml

const cloudImageDownloadTimeout = 4 * time.Hour

// EnsureCloudImage makes sure the configured cloud image exists locally.
func EnsureCloudImage(cmdExec platform.CommandExecutor, osConfig config.OSConfig, destPath string) error {
	// Check if file already exists
	if _, err := os.Stat(destPath); err == nil {
		log.Info("✓ Image already exists at %s, skipping download", destPath)
		return nil
	}

	if osConfig.ImageRef != "" {
		return pullCloudImageFromOCI(osConfig.ImageRef, osConfig.ImageName, destPath)
	}
	if osConfig.ImageURL != "" {
		return DownloadCloudImage(cmdExec, osConfig.ImageURL, destPath)
	}
	return errors.New("operating_system image source is not configured")
}

// DownloadCloudImage downloads a cloud image if it doesn't exist
func DownloadCloudImage(cmdExec platform.CommandExecutor, url, destPath string) error {
	// Check if file already exists
	if _, err := os.Stat(destPath); err == nil {
		log.Info("✓ Image already exists at %s, skipping download", destPath)
		return nil
	}

	// Create destination directory if it doesn't exist
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", destDir, err)
	}

	log.Info("Downloading cloud image from %s to %s...", url, destPath)
	stdout, stderr, err := platform.RunCommandInDir(cmdExec, "", "wget", []string{"-O", destPath, url}, cloudImageDownloadTimeout)
	if err != nil {
		return fmt.Errorf("failed to download image: %w, output: %s", err, platform.CombinedCmdOutput(stdout, stderr))
	}

	log.Info("✓ Downloaded image to %s", destPath)
	return nil
}

func pullOCICloudImage(imageRef, imageName, destPath string) error {
	parsedRef, err := registry.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("failed to parse OCI image reference %q: %w", imageRef, err)
	}
	if parsedRef.Reference == "" {
		return fmt.Errorf("OCI image reference %q must include a tag or digest", imageRef)
	}

	repo, err := remote.NewRepository(fmt.Sprintf("%s/%s", parsedRef.Registry, parsedRef.Repository))
	if err != nil {
		return fmt.Errorf("failed to create OCI repository client: %w", err)
	}

	ociClient := &auth.Client{
		Client: retry.DefaultClient,
		Cache:  auth.NewCache(),
	}
	if store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{}); err == nil {
		ociClient.Credential = credentials.Credential(store)
	} else {
		log.Debug("Unable to load docker credentials for OCI pull: %v", err)
	}
	repo.Client = ociClient

	tempDir, err := os.MkdirTemp("", "dpu-sim-oras-pull-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir for OCI pull: %w", err)
	}
	defer os.RemoveAll(tempDir)

	dstStore, err := file.New(tempDir)
	if err != nil {
		return fmt.Errorf("failed to create ORAS file store: %w", err)
	}
	defer dstStore.Close()
	dstStore.IgnoreNoName = true

	log.Info("Pulling OCI cloud image %s...", imageRef)
	ctx := context.Background()
	root, err := oras.Resolve(ctx, repo, parsedRef.Reference, oras.DefaultResolveOptions)
	if err != nil {
		return fmt.Errorf("failed to resolve OCI cloud image %s: %w", imageRef, err)
	}

	totalBytes := estimateOCIPullTotalBytes(ctx, repo, root)
	start := time.Now()
	var copiedBytes atomic.Int64

	countingDst := &progressStorage{
		Storage: dstStore,
		copied:  &copiedBytes,
	}

	copyOpts := oras.DefaultCopyGraphOptions
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				logOCIPullProgress(copiedBytes.Load(), totalBytes, start)
			}
		}
	}()

	// CopyGraph materializes artifact files without tagging the destination.
	if err := oras.CopyGraph(ctx, repo, countingDst, root, copyOpts); err != nil {
		close(done)
		return fmt.Errorf("failed to pull OCI cloud image %s: %w", imageRef, err)
	}
	close(done)
	logOCIPullProgress(copiedBytes.Load(), totalBytes, start)

	sourcePath, err := findPulledCloudImage(tempDir, imageName)
	if err != nil {
		return err
	}
	if err := copyFile(sourcePath, destPath); err != nil {
		return fmt.Errorf("failed to stage pulled cloud image %s: %w", sourcePath, err)
	}

	log.Info("✓ Pulled OCI cloud image %s to %s", imageRef, destPath)
	return nil
}

func estimateOCIPullTotalBytes(ctx context.Context, repo *remote.Repository, root ocispec.Descriptor) int64 {
	total := root.Size
	rc, err := repo.Fetch(ctx, root)
	if err != nil {
		return total
	}
	defer rc.Close()

	var manifest ocispec.Manifest
	if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
		return total
	}
	if manifest.Config.Size > 0 {
		total += manifest.Config.Size
	}
	for _, layer := range manifest.Layers {
		if layer.Size > 0 {
			total += layer.Size
		}
	}
	return total
}

func logOCIPullProgress(copiedBytes int64, totalBytes int64, start time.Time) {
	elapsed := time.Since(start)
	if elapsed <= 0 {
		elapsed = time.Second
	}
	speedBytesPerSec := float64(copiedBytes) / elapsed.Seconds()
	if totalBytes > 0 {
		pct := float64(copiedBytes) / float64(totalBytes) * 100
		if pct > 100 {
			pct = 100
		}
		log.Info("Pull progress: %s / %s (%.0f%%), %s/s, %s elapsed",
			formatBytes(copiedBytes), formatBytes(totalBytes), pct, formatBytes(int64(speedBytesPerSec)), elapsed.Round(time.Second))
		return
	}
	log.Info("Pull progress: %s downloaded, %s/s, %s elapsed",
		formatBytes(copiedBytes), formatBytes(int64(speedBytesPerSec)), elapsed.Round(time.Second))
}

func formatBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(size)
	unit := "B"
	for _, next := range units {
		value /= 1024
		unit = next
		if value < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

type progressStorage struct {
	content.Storage
	copied *atomic.Int64
}

func (p *progressStorage) Push(ctx context.Context, expected ocispec.Descriptor, reader io.Reader) error {
	return p.Storage.Push(ctx, expected, &countingReader{
		reader: reader,
		copied: p.copied,
	})
}

type countingReader struct {
	reader io.Reader
	copied *atomic.Int64
}

func (c *countingReader) Read(buf []byte) (int, error) {
	n, err := c.reader.Read(buf)
	if n > 0 {
		c.copied.Add(int64(n))
	}
	return n, err
}

func findPulledCloudImage(rootDir, imageName string) (string, error) {
	files, err := listRegularFiles(rootDir)
	if err != nil {
		return "", err
	}

	if imageName != "" {
		expected := make([]string, 0, 1)
		for _, path := range files {
			if filepath.Base(path) == imageName {
				expected = append(expected, path)
			}
		}
		if len(expected) == 1 {
			return expected[0], nil
		}
		if len(expected) > 1 {
			return "", fmt.Errorf("multiple pulled files match image_name %q: %s", imageName, strings.Join(expected, ", "))
		}
	}

	qcow2Files := make([]string, 0, 1)
	for _, path := range files {
		if strings.EqualFold(filepath.Ext(path), ".qcow2") {
			qcow2Files = append(qcow2Files, path)
		}
	}
	if len(qcow2Files) == 1 {
		return qcow2Files[0], nil
	}
	if len(qcow2Files) == 0 {
		return "", fmt.Errorf("no %q or .qcow2 file found in pulled OCI artifact", imageName)
	}
	return "", fmt.Errorf("multiple .qcow2 files found in pulled OCI artifact: %s", strings.Join(qcow2Files, ", "))
}

func listRegularFiles(rootDir string) ([]string, error) {
	paths := make([]string, 0)
	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect pulled OCI artifact files: %w", err)
	}
	return paths, nil
}

func copyFile(sourcePath, destPath string) error {
	src, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destPath), filepath.Base(destPath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// CreateVMDisk creates a disk image for a VM using qemu-img
func CreateVMDisk(cmdExec platform.CommandExecutor, vmName string, sizeGB int, baseImage string) (string, error) {
	diskPath := filepath.Join(DefaultImageDir, fmt.Sprintf("%s.qcow2", vmName))

	// Check if disk already exists
	if _, err := os.Stat(diskPath); err == nil {
		log.Info("✓ Disk for VM %s already exists at %s", vmName, diskPath)
		return diskPath, nil
	}

	// Create image directory if it doesn't exist
	if err := os.MkdirAll(DefaultImageDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create image directory: %w", err)
	}

	baseVirtualSizeBytes, err := imageVirtualSizeBytes(cmdExec, baseImage)
	if err != nil {
		return "", fmt.Errorf("failed to inspect base image size %s: %w", baseImage, err)
	}
	requestedSizeBytes := int64(sizeGB) * 1024 * 1024 * 1024

	log.Debug("Creating disk for %s based on %s...", vmName, baseImage)
	out, errOut, err := platform.RunCommandInDir(cmdExec, "", "qemu-img", []string{
		"create", "-f", "qcow2", "-F", "qcow2", "-b", baseImage, diskPath,
	}, 30*time.Minute)
	if err != nil {
		return "", fmt.Errorf("failed to create disk: %w, output: %s", err, platform.CombinedCmdOutput(out, errOut))
	}

	// Treat disk_size as a minimum. Older code always ran resize, which failed
	// when disk_size was smaller than the backing image virtual size. For qcow2
	// overlays we only grow when requested size exceeds the base image size.
	if requestedSizeBytes > baseVirtualSizeBytes {
		ro, rerr, resizeErr := platform.RunCommandInDir(cmdExec, "", "qemu-img", []string{"resize", diskPath, fmt.Sprintf("%dG", sizeGB)}, 30*time.Minute)
		if resizeErr != nil {
			return "", fmt.Errorf("failed to resize disk %s: %w, output: %s", diskPath, resizeErr, platform.CombinedCmdOutput(ro, rerr))
		}
	}

	log.Info("✓ Created disk for %s: %s", vmName, diskPath)
	return diskPath, nil
}

// ifaceNameAndMAC is the desired guest interface name and the deterministic MAC
type ifaceNameAndMAC struct {
	Name string
	MAC  string
}

// getInterfaceNamesAndMACs returns (name, MAC)
// MACs are generated the same way as when the VMs are created so udev can match ATTR{address} and set NAME.
func getInterfaceNamesAndMACs(cfg *config.Config, vmConfig config.VMConfig) []ifaceNameAndMAC {
	var out []ifaceNameAndMAC
	for _, net := range cfg.Networks {
		if net.AttachTo != "any" && net.AttachTo != vmConfig.Type {
			continue
		}
		if net.Type == config.GatewayNetworkName {
			if vmConfig.Type != config.DpuType || vmConfig.Host == "" {
				continue
			}
		}
		mac := GenerateMACForNetwork(vmConfig.Name, net.Type)
		if net.Type == config.K8sNetworkName && vmConfig.K8sNodeMAC != "" {
			mac = vmConfig.K8sNodeMAC
		}
		out = append(out, ifaceNameAndMAC{Name: net.Type, MAC: mac})
	}
	numPairs := cfg.GetHostToDpuNumPairs()
	mappings := cfg.GetHostDPUMappings()
	if vmConfig.Type == config.HostType {
		for _, mapping := range mappings {
			if mapping.Host.Name == vmConfig.Name {
				for idx := 0; idx < numPairs; idx++ {
					out = append(out, ifaceNameAndMAC{
						Name: fmt.Sprintf(network.HostDataIfFmt, idx),
						MAC:  network.GenerateMACForHostToDpu(vmConfig.Name, config.HostType, idx),
					})
				}
				break
			}
		}
	}
	if vmConfig.Type == config.DpuType {
		for _, mapping := range mappings {
			for _, conn := range mapping.Connections {
				if conn.DPU.Name == vmConfig.Name {
					for idx := 0; idx < numPairs; idx++ {
						out = append(out, ifaceNameAndMAC{
							Name: fmt.Sprintf(network.DPUDataIfFmt, idx),
							MAC:  network.GenerateMACForHostToDpu(vmConfig.Name, config.DpuType, idx),
						})
					}
					break
				}
			}
		}
	}
	return out
}

func imageVirtualSizeBytes(cmdExec platform.CommandExecutor, imagePath string) (int64, error) {
	stdout, stderr, err := platform.RunCommandInDir(cmdExec, "", "qemu-img", []string{"info", "--output=json", imagePath}, 2*time.Minute)
	if err != nil {
		return 0, fmt.Errorf("qemu-img info failed: %w, output: %s", err, platform.CombinedCmdOutput(stdout, stderr))
	}

	var info struct {
		VirtualSize int64 `json:"virtual-size"`
	}
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		return 0, fmt.Errorf("failed to parse qemu-img info json: %w", err)
	}
	if info.VirtualSize <= 0 {
		return 0, fmt.Errorf("invalid virtual-size reported for %s", imagePath)
	}

	return info.VirtualSize, nil
}

// CreateCloudInitISO creates a cloud-init ISO for VM initialization.
// cfg must be non-nil; udev rules rename interfaces by MAC to common names (mgmt, k8s, eth0-0, rep0-0, etc.).
func CreateCloudInitISO(cmdExec platform.CommandExecutor, sshConfig config.SSHConfig, vmConfig config.VMConfig, cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config is nil")
	}

	vmName := vmConfig.Name
	isoPath := filepath.Join(DefaultImageDir, fmt.Sprintf("%s-cloud-init.iso", vmName))

	if _, err := os.Stat(isoPath); err == nil {
		log.Info("✓ Cloud-init ISO for %s already exists at %s", vmName, isoPath)
		return isoPath, nil
	}

	// Create temporary directory for cloud-init files
	tempDir, err := os.MkdirTemp("", fmt.Sprintf("cloud-init-%s-", vmName))
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Create meta-data file
	metaDataPath := filepath.Join(tempDir, "meta-data")
	metaData := generateMetaData(vmName)
	if err := os.WriteFile(metaDataPath, []byte(metaData), 0o644); err != nil {
		return "", fmt.Errorf("failed to write meta-data: %w", err)
	}

	// Read SSH public key
	pubKeyPath := sshConfig.KeyPath + ".pub"
	pubKeyData, err := os.ReadFile(pubKeyPath)
	if err != nil {
		return "", fmt.Errorf("failed to read SSH public key: %w", err)
	}

	ifaceNameMACs := getInterfaceNamesAndMACs(cfg, vmConfig)
	userData := generateUserData(string(pubKeyData), sshConfig.User, sshConfig.Password, ifaceNameMACs, cfg, vmConfig)
	userDataPath := filepath.Join(tempDir, "user-data")
	if err := os.WriteFile(userDataPath, []byte(userData), 0o644); err != nil {
		return "", fmt.Errorf("failed to write user-data: %w", err)
	}

	out, errOut, err := platform.RunCommandInDir(cmdExec, "", "genisoimage", []string{
		"-output", isoPath, "-volid", "cidata", "-joliet", "-rock", userDataPath, metaDataPath,
	}, 10*time.Minute)
	if err != nil {
		return "", fmt.Errorf("failed to create cloud-init ISO: %w, output: %s", err, platform.CombinedCmdOutput(out, errOut))
	}

	log.Info("✓ Created cloud-init ISO: %s", isoPath)
	return isoPath, nil
}

// generateMetaData generates cloud-init meta-data content with the name of the VM.
func generateMetaData(vmName string) string {
	var sb strings.Builder

	sb.WriteString("instance-id: " + vmName + "\n")
	sb.WriteString("local-hostname: " + vmName + "\n")
	return sb.String()
}

// generateUserData generates cloud-init user-data content that sets ssh keys, passwords, updates packages, and disables
// zram. ZRAM enables swap, which is not desirable for k8s, hense we disable it partially here. If ifaceNameMACs is non-empty,
// udev rules rename interfaces (mgmt, k8s, eth0-0, etc.). NetworkManager is kept but only mgmt is managed (DHCP); other interfaces
// are unmanaged. cfg must be non-nil.
func generateUserData(sshPubKey, username, password string, ifaceNameMACs []ifaceNameAndMAC, cfg *config.Config, vmConfig config.VMConfig) string {
	var sb strings.Builder

	sb.WriteString("#cloud-config\n")

	sb.WriteString("users:\n")
	sb.WriteString("  - name: " + username + "\n")
	sb.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
	sb.WriteString("    groups: wheel\n")
	sb.WriteString("    shell: /bin/bash\n")
	sb.WriteString("    ssh_authorized_keys:\n")
	sb.WriteString("      - " + strings.TrimSpace(sshPubKey) + "\n")

	sb.WriteString("\n# Set password for console access (password: " + password + ")\n")
	sb.WriteString("chpasswd:\n")
	sb.WriteString("  list: |\n")
	sb.WriteString("    " + username + ":" + password + "\n")
	sb.WriteString("  expire: false\n")

	sb.WriteString("\n# Enable password authentication for emergency access\n")
	sb.WriteString("ssh_pwauth: true\n")

	sb.WriteString("\n# Update packages\n")
	sb.WriteString("package_update: true\n")
	sb.WriteString("package_upgrade: false\n")

	sb.WriteString("\n# Additional packages\n")
	sb.WriteString("packages:\n")
	sb.WriteString("  - curl\n")
	sb.WriteString("  - wget\n")

	sb.WriteString("\n# ZRAM configuration\n")
	sb.WriteString("write_files:\n")
	sb.WriteString("  - path: /etc/systemd/zram-generator.conf\n")
	sb.WriteString("    content: \"\"\n")
	sb.WriteString("    permissions: \"0644\"\n")

	if len(ifaceNameMACs) > 0 {
		// udev rules: match by deterministic MAC (hash of VM name + type), set NAME to common name
		udevContent := "# dpu-sim: rename interfaces by MAC to common names\n"
		for _, m := range ifaceNameMACs {
			udevContent += fmt.Sprintf("ATTR{address}==%q, SUBSYSTEM==\"net\", ACTION==\"add\", NAME=%q\n", m.MAC, m.Name)
		}
		sb.WriteString("  - path: /etc/udev/rules.d/70-dpu-sim-ifnames.rules\n")
		sb.WriteString("    content: |\n")
		for _, line := range strings.Split(strings.TrimSuffix(udevContent, "\n"), "\n") {
			sb.WriteString("      " + line + "\n")
		}
		sb.WriteString("    permissions: \"0644\"\n")
		// Boot script: udev names devices on later boots; the script also brings
		// unmanaged links up, including the gateway representor not owned by CNI.
		// Use read < file to get MAC without newline; cat would include newline and break the comparison.
		sb.WriteString("  - path: /etc/dpu-sim-rename-ifaces.sh\n")
		sb.WriteString("    content: |\n")
		sb.WriteString("      #!/bin/bash\n")
		sb.WriteString("      set -e\n")
		sb.WriteString("      # dpu-sim: name and activate unmanaged interfaces at boot\n")
		for _, m := range ifaceNameMACs {
			sb.WriteString("      for d in /sys/class/net/*; do\n")
			sb.WriteString("        [ -f \"$d/address\" ] || continue\n")
			sb.WriteString("        ifname=$(basename \"$d\")\n")
			sb.WriteString("        [ \"$ifname\" = lo ] && continue\n")
			sb.WriteString("        read -r mac < \"$d/address\"\n")
			sb.WriteString(fmt.Sprintf("        if [ \"$mac\" = \"%s\" ]; then\n", m.MAC))
			sb.WriteString(fmt.Sprintf("          if [ \"$ifname\" != \"%s\" ]; then\n", m.Name))
			sb.WriteString("            ip link set dev \"$ifname\" down\n")
			sb.WriteString(fmt.Sprintf("            ip link set dev \"$ifname\" name \"%s\"\n", m.Name))
			sb.WriteString("          fi\n")
			sb.WriteString(fmt.Sprintf("          ip link set dev \"%s\" up\n", m.Name))
			sb.WriteString("          break\n")
			sb.WriteString("        fi\n")
			sb.WriteString("      done\n")
		}
		sb.WriteString("    permissions: \"0755\"\n")
		sb.WriteString("  - path: /etc/systemd/system/dpu-sim-interfaces.service\n")
		sb.WriteString("    permissions: \"0644\"\n")
		sb.WriteString("    content: |\n")
		sb.WriteString("      [Unit]\n")
		sb.WriteString("      Description=Activate simulator interfaces after udev naming\n")
		sb.WriteString("      Wants=systemd-udev-settle.service\n")
		sb.WriteString("      After=systemd-udev-settle.service\n")
		sb.WriteString("      Before=NetworkManager.service dpu-sim-k8s-ip.service kubelet.service crio.service\n")
		sb.WriteString("      [Service]\n")
		sb.WriteString("      Type=oneshot\n")
		sb.WriteString("      RemainAfterExit=yes\n")
		sb.WriteString("      ExecStart=/etc/dpu-sim-rename-ifaces.sh\n")
		sb.WriteString("      [Install]\n")
		sb.WriteString("      WantedBy=multi-user.target\n")
	}

	// NetworkManager: manage only mgmt (DHCP). Other interfaces (k8s, host-to-dpu links) are unmanaged by MAC so it persists if interface names change.
	if len(ifaceNameMACs) > 0 {
		var unmanagedSpecs []string
		for _, m := range ifaceNameMACs {
			if m.Name != config.MgmtNetworkName {
				unmanagedSpecs = append(unmanagedSpecs, "mac:"+strings.ToLower(m.MAC))
			}
		}
		// OVS/CNI interfaces used by OVN; keep them unmanaged so NetworkManager does not touch them.
		unmanagedSpecs = append(unmanagedSpecs, "interface-name:br-int", "interface-name:brk8s", "interface-name:brgateway", "interface-name:cni0", "interface-name:ovn-k8s-mp0")
		if len(unmanagedSpecs) > 0 {
			sb.WriteString("  - path: /etc/NetworkManager/conf.d/90-dpu-sim-unmanaged.conf\n")
			sb.WriteString("    content: |\n")
			sb.WriteString("      [keyfile]\n")
			sb.WriteString("      unmanaged-devices=" + strings.Join(unmanagedSpecs, ";") + "\n")
			sb.WriteString("    permissions: \"0644\"\n")
		}

		// k8s static IP and route to gateway: set once at boot via a oneshot service so no daemon
		// keeps managing the interface. That allows OVN (or similar) to move the IP to another
		// device (e.g. br-ex) at runtime without a manager re-applying it back to k8s.
		k8sNet := cfg.GetNetworkByType(config.K8sNetworkName)
		if k8sNet != nil && vmConfig.K8sNodeIP != "" && k8sNet.SubnetMask != "" {
			prefix, err := config.PrefixLenFromSubnetMask(k8sNet.SubnetMask)
			if err == nil {
				cidr := fmt.Sprintf("%s/%d", vmConfig.K8sNodeIP, prefix)
				gateway := k8sNet.Gateway
				execStart := fmt.Sprintf("ip link set dev k8s up && ip -4 addr replace %s dev k8s", cidr)
				if gateway != "" {
					// Default route via k8s gateway with metric 200 so mgmt stays primary.
					execStart += fmt.Sprintf(" && ip -4 route replace default via %s dev k8s metric 200", gateway)
				}
				sb.WriteString("  - path: /etc/systemd/system/dpu-sim-k8s-ip.service\n")
				sb.WriteString("    content: |\n")
				sb.WriteString("      [Unit]\n")
				sb.WriteString("      Description=Set static IP on k8s interface (dpu-sim)\n")
				sb.WriteString("      After=network-online.target\n")
				sb.WriteString("      Wants=network-online.target\n")
				sb.WriteString("      [Service]\n")
				sb.WriteString("      Type=oneshot\n")
				sb.WriteString("      RemainAfterExit=yes\n")
				sb.WriteString(fmt.Sprintf("      ExecStart=/bin/sh -c '%s'\n", execStart))
				sb.WriteString("      [Install]\n")
				sb.WriteString("      WantedBy=multi-user.target\n")
				sb.WriteString("    permissions: \"0644\"\n")
			}
		}
	}

	// A oneshot assigns the gateway IP to the gateway NIC before OVN moves its address to OVS.
	gatewayIP := ""
	if cfg.IsOffloadDPU() && vmConfig.Type == config.DpuType {
		gatewayIP, _ = cfg.VMGatewayIP(vmConfig.Name)
	}
	if gatewayIP != "" {
		_, subnet, _ := net.ParseCIDR(cfg.DPUHostGatewaySubnet())
		prefix, _ := subnet.Mask.Size()
		sb.WriteString("  - path: /etc/systemd/system/dpu-sim-dpu-gateway.service\n")
		sb.WriteString("    permissions: \"0644\"\n")
		sb.WriteString("    content: |\n")
		sb.WriteString("      [Unit]\n")
		sb.WriteString("      Description=Set static DPU gateway address\n")
		sb.WriteString("      Requires=dpu-sim-interfaces.service\n")
		sb.WriteString("      After=dpu-sim-interfaces.service\n")
		sb.WriteString("      Before=kubelet.service crio.service\n")
		sb.WriteString("      [Service]\n")
		sb.WriteString("      Type=oneshot\n")
		sb.WriteString("      RemainAfterExit=yes\n")
		sb.WriteString("      ExecStart=/usr/sbin/ip link set dev gateway up\n")
		sb.WriteString(fmt.Sprintf("      ExecStart=/usr/sbin/ip addr replace %s/%d dev gateway\n", gatewayIP, prefix))
		sb.WriteString("      [Install]\n")
		sb.WriteString("      WantedBy=multi-user.target\n")
	}

	sb.WriteString("\n# Start services\n")
	sb.WriteString("runcmd:\n")
	sb.WriteString("  - systemctl enable sshd\n")
	sb.WriteString("  - systemctl start sshd\n")
	sb.WriteString("  - systemctl daemon-reload\n")
	sb.WriteString("  - systemctl restart zram-generator.service\n")

	if len(ifaceNameMACs) > 0 {
		sb.WriteString("  - udevadm control --reload-rules\n")
		sb.WriteString("  - udevadm trigger --subsystem-match=net\n")
		sb.WriteString("  - systemctl enable --now dpu-sim-interfaces.service\n")
		// Restart NM so it loads conf.d/90-dpu-sim-unmanaged.conf (unmanaged-devices).
		sb.WriteString("  - systemctl restart NetworkManager\n")
		sb.WriteString("  - systemctl daemon-reload\n")
		sb.WriteString("  - systemctl enable dpu-sim-k8s-ip.service\n")
		sb.WriteString("  - systemctl start dpu-sim-k8s-ip.service\n")
	}

	if gatewayIP != "" {
		sb.WriteString("  - systemctl enable --now dpu-sim-dpu-gateway.service\n")
	}
	return sb.String()
}

// DeleteVMDisk deletes a VM disk image
func DeleteVMDisk(vmName string) error {
	diskPath := filepath.Join(DefaultImageDir, fmt.Sprintf("%s.qcow2", vmName))

	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		log.Info("✓ Disk for %s does not exist, skipping deletion", vmName)
		return nil
	}

	if err := os.Remove(diskPath); err != nil {
		return fmt.Errorf("failed to delete disk %s: %w", diskPath, err)
	}

	log.Info("✓ Deleted disk: %s", diskPath)
	return nil
}

// DeleteCloudInitISO deletes a cloud-init ISO
func DeleteCloudInitISO(vmName string) error {
	isoPath := filepath.Join(DefaultImageDir, fmt.Sprintf("%s-cloud-init.iso", vmName))

	if _, err := os.Stat(isoPath); os.IsNotExist(err) {
		log.Info("✓ Cloud-init ISO for %s does not exist, skipping deletion", vmName)
		return nil
	}

	if err := os.Remove(isoPath); err != nil {
		return fmt.Errorf("failed to delete cloud-init ISO %s: %w", isoPath, err)
	}

	log.Info("✓ Deleted cloud-init ISO: %s", isoPath)
	return nil
}

// DeleteUEFINvram deletes any per-VM UEFI NVRAM files.
func DeleteUEFINvram(vmName string) error {
	pattern := filepath.Join("/var/lib/libvirt/qemu/nvram", fmt.Sprintf("%s_VARS*", vmName))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to glob UEFI NVRAM files for %s: %w", vmName, err)
	}
	if len(matches) == 0 {
		fmt.Printf("✓ UEFI NVRAM for %s does not exist, skipping deletion\n", vmName)
		return nil
	}

	for _, path := range matches {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to delete UEFI NVRAM %s: %w", path, err)
		}
		fmt.Printf("✓ Deleted UEFI NVRAM: %s\n", path)
	}

	return nil
}

// GetImagePath returns the path where an OS image should be stored
func GetImagePath(osConfig config.OSConfig) string {
	filename := filepath.Base(osConfig.ImageName)
	return filepath.Join(DefaultImageDir, filename)
}
