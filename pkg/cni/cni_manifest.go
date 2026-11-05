package cni

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const writableCNIBinDir = "/var/lib/cni/bin"

func (m *CNIManager) shouldUseWritableCNIBinDir() bool {
	// Kind node images stage plugins under /opt/cni/bin (see linux.InstallKindCNIPlugins).
	if m.config.IsKindMode() {
		return false
	}

	// Read-only / bootc hosts: stage CNI binaries under /var/lib/cni/bin and point
	// Multus there. Normal VM installs mirror packaged plugins into /opt/cni/bin
	// with real copies (linux.EnsureCRIOCNIPluginPaths) so Multus thick can use
	// default binDir /opt/cni/bin: symlinks would break inside the Multus mount
	// namespace, and OVN-Kubernetes only installs ovn-k8s-cni-overlay under
	// /opt/cni/bin — that binary is absent from /var/lib/cni/bin.
	if strings.TrimSpace(m.config.OperatingSystem.ImageRef) != "" {
		return true
	}

	for _, node := range m.config.BareMetal {
		if node.Bootc != nil && node.Bootc.Enabled {
			return true
		}
	}

	return false
}

func downloadManifest(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create manifest download request for %s: %w", url, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download manifest from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download manifest from %s: HTTP %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest body: %w", err)
	}

	return body, nil
}

func rewriteCNIBinPath(manifest []byte, hostPath string) []byte {
	content := string(manifest)
	hostMountPath := "/host" + hostPath
	content = strings.ReplaceAll(content, "/host/opt/cni/bin", hostMountPath)
	content = strings.ReplaceAll(content, "/opt/cni/bin", hostPath)
	return []byte(content)
}

func rewriteCNIHostPathOnly(manifest []byte, hostPath string) []byte {
	content := string(manifest)
	content = strings.ReplaceAll(content, "path: /opt/cni/bin", fmt.Sprintf("path: %s", hostPath))
	return []byte(content)
}

// manifestImageRe matches container image references in a Kubernetes manifest,
// e.g. `image: ghcr.io/foo/bar:tag`, optionally quoted or in list form.
var manifestImageRe = regexp.MustCompile(`(?m)^\s*(?:-\s+)?image:\s*["']?([^\s"']+)`)

// imagesFromManifest returns the unique container images referenced by a
// Kubernetes manifest, in order of first appearance.
func imagesFromManifest(manifest []byte) []string {
	var images []string
	seen := map[string]bool{}
	for _, match := range manifestImageRe.FindAllStringSubmatch(string(manifest), -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			images = append(images, match[1])
		}
	}
	return images
}

// ManifestImages downloads the manifest at url and returns the unique
// container images it deploys. Used to preload those images into cluster
// nodes before the manifest is applied.
func ManifestImages(url string) ([]string, error) {
	manifest, err := downloadManifest(url)
	if err != nil {
		return nil, err
	}
	return imagesFromManifest(manifest), nil
}
