package k8s

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
	"github.com/stretchr/testify/require"
)

// This replaces an existing --node-ip while keeping other flags; this output must match exactly.
func TestMergeNodeIPIntoKubeadmArgsLine(t *testing.T) {
	const ip = "192.168.123.12"
	in := `KUBELET_KUBEADM_ARGS="--foo=1 --node-ip=192.168.120.25"
`
	want := `KUBELET_KUBEADM_ARGS="--foo=1 --node-ip=` + ip + `"
`
	out, changed, err := mergeNodeIPIntoKubeadmArgsLine(in, ip)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected change")
	}
	if out != want {
		t.Fatalf("output mismatch\ngot:  %q\nwant: %q", out, want)
	}
	if !strings.Contains(out, "--foo=1") {
		t.Fatalf("expected original flag preserved: %q", out)
	}
	if strings.Contains(out, "192.168.120.25") {
		t.Fatalf("old node-ip should be gone: %q", out)
	}
	if strings.Count(out, "--node-ip=") != 1 {
		t.Fatalf("expected single --node-ip: %q", out)
	}
}

// Test duplicate identical node-ip tokens in the input. Result must collapse to one without changing the rest of the line.
func TestMergeNodeIPIntoKubeadmArgsLineDedupeDuplicateDesired(t *testing.T) {
	const ip = "192.168.123.12"
	in := `KUBELET_KUBEADM_ARGS="--foo=1 --node-ip=` + ip + ` --node-ip=` + ip + `"
`
	want := `KUBELET_KUBEADM_ARGS="--foo=1 --node-ip=` + ip + `"
`
	out, changed, err := mergeNodeIPIntoKubeadmArgsLine(in, ip)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected change")
	}
	if out != want {
		t.Fatalf("output mismatch\ngot:  %q\nwant: %q", out, want)
	}
	if strings.Count(out, "--node-ip=") != 1 {
		t.Fatalf("expected single --node-ip: %q", out)
	}
}

// When there is no --node-ip present, then we expect it to append node-ip and preserves the rest of the arguments.
func TestMergeNodeIPIntoKubeadmArgsLineAppend(t *testing.T) {
	const ip = "192.168.123.12"
	in := `KUBELET_KUBEADM_ARGS="--cgroup-driver=systemd"
`
	want := `KUBELET_KUBEADM_ARGS="--cgroup-driver=systemd --node-ip=` + ip + `"
`
	out, changed, err := mergeNodeIPIntoKubeadmArgsLine(in, ip)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected change")
	}
	if out != want {
		t.Fatalf("output mismatch\ngot:  %q\nwant: %q", out, want)
	}
	if !strings.Contains(out, "--cgroup-driver=systemd") {
		t.Fatalf("expected --cgroup-driver=systemd preserved: %q", out)
	}
	if strings.Count(out, "--node-ip=") != 1 {
		t.Fatalf("expected single --node-ip: %q", out)
	}
}

// Already correct node-ip yields no changes (changed=false) and there should be byte-for-byte identical content.
func TestMergeNodeIPIntoKubeadmArgsLineNoop(t *testing.T) {
	const ip = "192.168.123.12"
	in := `KUBELET_KUBEADM_ARGS="--x=1 --node-ip=` + ip + `"
`
	out, changed, err := mergeNodeIPIntoKubeadmArgsLine(in, ip)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("unexpected change: %q", out)
	}
	if out != in {
		t.Fatalf("noop should return input unchanged\ngot:  %q\nwant: %q", out, in)
	}
}

// Already correct node-ip before other additional flags must not reorder or set the changed flag (avoids unnecessary kubelet restart).
func TestMergeNodeIPIntoKubeadmArgsLineNoopNodeIPFirst(t *testing.T) {
	const ip = "192.168.123.12"
	in := `KUBELET_KUBEADM_ARGS="--node-ip=` + ip + ` --x=1"
`
	out, changed, err := mergeNodeIPIntoKubeadmArgsLine(in, ip)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("unexpected change: %q", out)
	}
	if out != in {
		t.Fatalf("noop should preserve flag order\ngot:  %q\nwant: %q", out, in)
	}
}

// Trailing whitespace or CR after the closing quote (e.g. CRLF split) must not break parsing;
// noop must not rewrite when there is only the --node-ip difference from normalized line.
func TestMergeNodeIPIntoKubeadmArgsLineQuotedValueTrailingJunk(t *testing.T) {
	const ip = "192.168.123.12"
	t.Run("trailing spaces after quote noop", func(t *testing.T) {
		in := `KUBELET_KUBEADM_ARGS="--x=1 --node-ip=` + ip + `"   ` + "\n"
		out, changed, err := mergeNodeIPIntoKubeadmArgsLine(in, ip)
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			t.Fatalf("unexpected change: %q", out)
		}
		if out != in {
			t.Fatalf("noop should keep file as-is\ngot:  %q\nwant: %q", out, in)
		}
	})
	t.Run("CR after quote before newline", func(t *testing.T) {
		in := "KUBELET_KUBEADM_ARGS=\"--cgroup-driver=systemd\"\r\n"
		want := `KUBELET_KUBEADM_ARGS="--cgroup-driver=systemd --node-ip=` + ip + `"` + "\n"
		out, changed, err := mergeNodeIPIntoKubeadmArgsLine(in, ip)
		if err != nil {
			t.Fatal(err)
		}
		if !changed {
			t.Fatal("expected change")
		}
		if out != want {
			t.Fatalf("output mismatch\ngot:  %q\nwant: %q", out, want)
		}
	})
}

// Table-driven checks for invalid IP, missing KUBELET_KUBEADM_ARGS= line, and unquoted value.
func TestMergeNodeIPIntoKubeadmArgsLineErrors(t *testing.T) {
	validLine := `KUBELET_KUBEADM_ARGS="--node-ip=10.0.0.1"`
	validIP := "192.168.1.2"

	tests := []struct {
		name        string
		fileContent string
		nodeIP      string
		wantSubstr  string // distinctive fragment of error message
	}{
		{
			name:        "invalid IP",
			fileContent: validLine + "\n",
			nodeIP:      "not-an-ip",
			wantSubstr:  `invalid node IP "not-an-ip"`,
		},
		{
			name:        "missing KUBELET_KUBEADM_ARGS line",
			fileContent: "# only comments\nOTHER=1\n",
			nodeIP:      validIP,
			wantSubstr:  "missing KUBELET_KUBEADM_ARGS=",
		},
		{
			name:        "unquoted value",
			fileContent: `KUBELET_KUBEADM_ARGS=--cgroup-driver=systemd` + "\n",
			nodeIP:      validIP,
			wantSubstr:  "expected quoted value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := mergeNodeIPIntoKubeadmArgsLine(tt.fileContent, tt.nodeIP)
			if err == nil {
				t.Fatal("expected non-nil error")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("error %q should contain %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

type waitNodeIPExecutor struct {
	platform.CommandExecutor
	path string
}

func (e waitNodeIPExecutor) ExecuteRetryWithTimeout(command string, _, _ time.Duration) (string, string, error) {
	c := exec.Command("sh", "-c", command)
	c.Env = append(os.Environ(), "PATH="+e.path+":"+os.Getenv("PATH"))
	b, err := c.CombinedOutput()
	return string(b), "", err
}

func TestWaitNodeInternalIPBeforeCNI(t *testing.T) {
	// Model a newly joined node: the API has its addresses but Ready is false.
	// Execute the actual remote shell predicate against a kubectl stand-in.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kubectl"), []byte(`#!/bin/sh
case "$*" in
  *'get node worker-1'*'jsonpath='*) printf '192.168.123.12 fd00::12' ;;
  *) echo 'node is NotReady until CNI installation' >&2; exit 1 ;;
esac
`), 0o755))
	e := waitNodeIPExecutor{path: dir}
	require.NoError(t, WaitNodeInternalIP(e, "worker-1", "192.168.123.12", time.Second))
	require.Error(t, WaitNodeInternalIP(e, "worker-1", "192.168.120.12", time.Second))
	require.Error(t, WaitNodeInternalIP(e, "worker-1; touch /tmp/invalid", "192.168.123.12", time.Second))
}
