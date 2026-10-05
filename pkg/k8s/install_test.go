package k8s

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

type initCommandRecorder struct {
	platform.CommandExecutor
	command string
}

func (e *initCommandRecorder) String() string { return "bootstrap-recorder" }
func (e *initCommandRecorder) ExecuteWithTimeout(command string, _ time.Duration) (string, string, error) {
	e.command = command
	return "", "", errors.New("stop before initializing a real cluster")
}

func TestBootstrapCanOmitKubeProxyForOVN(t *testing.T) {
	for _, skip := range []bool{false, true} {
		e := &initCommandRecorder{}
		m := NewK8sMachineManager(&config.Config{})
		_, err := m.InitializeControlPlane(e, "master-1", "192.168.123.11", "10.244.0.0/16", "10.245.0.0/16", "192.168.123.11:6443", nil, skip)
		if err == nil {
			t.Fatal("expected recorder to stop bootstrap")
		}
		if got := strings.Contains(e.command, "--skip-phases=addon/kube-proxy"); got != skip {
			t.Fatalf("skip proxy %v: %s", skip, e.command)
		}
	}
}

type privilegedFileRecorder struct {
	platform.CommandExecutor
	tempPath string
	path     string
	content  []byte
	mode     os.FileMode
	command  string
	removed  string
}

func (e *privilegedFileRecorder) Execute(command string) (string, string, error) {
	if command != "mktemp /tmp/dpu-sim-ovs.XXXXXX" {
		return "", "", errors.New("unexpected command")
	}
	return e.tempPath + "\n", "", nil
}

func (e *privilegedFileRecorder) WriteFile(path string, content []byte, mode os.FileMode) error {
	e.path = path
	e.content = append([]byte(nil), content...)
	e.mode = mode
	return nil
}

func (e *privilegedFileRecorder) RunCmd(_ log.Level, name string, args ...string) error {
	e.command = strings.Join(append([]string{name}, args...), " ")
	return nil
}

func (e *privilegedFileRecorder) RemoveAll(path string) error {
	e.removed = path
	return nil
}

func TestWritePrivilegedFileUsesSudoInstall(t *testing.T) {
	e := &privilegedFileRecorder{tempPath: "/tmp/dpu-sim-ovs.test"}
	content := []byte("runtime ACL helper\n")
	if err := writePrivilegedFile(e, "/etc/systemd/example.conf", content, 0o644); err != nil {
		t.Fatalf("writePrivilegedFile() error = %v", err)
	}
	if e.path != e.tempPath {
		t.Fatalf("temporary path = %q, want %q", e.path, e.tempPath)
	}
	if string(e.content) != string(content) || e.mode != 0o644 {
		t.Fatalf("temporary payload = %q/%o, want %q/%o", e.content, e.mode, content, 0o644)
	}
	wantCommand := "sudo install -m 644 /tmp/dpu-sim-ovs.test /etc/systemd/example.conf"
	if e.command != wantCommand {
		t.Fatalf("install command = %q, want %q", e.command, wantCommand)
	}
	if e.removed != e.tempPath {
		t.Fatalf("removed path = %q, want %q", e.removed, e.tempPath)
	}
}
