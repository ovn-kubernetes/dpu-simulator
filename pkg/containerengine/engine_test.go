package containerengine

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

type runCmdCall struct {
	name string
	args []string
	// FromRunCmdWithExtraEnv is true only for RunCmdWithExtraEnv; ExtraEnv is then
	// a copy of the provided slice (may be nil/empty). Plain RunCmd leaves both unset.
	fromRunCmdWithExtraEnv bool
	extraEnv               []string
}

type execResult struct {
	stdout string
	stderr string
	err    error
}

type fakeExecutor struct {
	runCalls  []runCmdCall
	execCalls []string

	runErr    error
	execByCmd map[string]execResult
}

func (f *fakeExecutor) WaitUntilReady(timeout time.Duration) error { return nil }

func (f *fakeExecutor) Execute(command string) (stdout, stderr string, err error) {
	f.execCalls = append(f.execCalls, command)
	if r, ok := f.execByCmd[command]; ok {
		return r.stdout, r.stderr, r.err
	}
	return "", "", fmt.Errorf("unexpected execute command: %s", command)
}

func (f *fakeExecutor) ExecuteWithTimeout(command string, timeout time.Duration) (stdout, stderr string, err error) {
	return f.Execute(command)
}

func (f *fakeExecutor) ExecuteRetryWithTimeout(command string, interval, timeout time.Duration) (stdout, stderr string, err error) {
	return f.Execute(command)
}

func (f *fakeExecutor) RunCmd(level log.Level, name string, args ...string) error {
	return f.recordRunCmd(false, nil, name, args...)
}

func (f *fakeExecutor) RunCmdInDir(level log.Level, dir string, name string, args ...string) error {
	return f.RunCmd(level, name, args...)
}

func (f *fakeExecutor) RunCmdWithExtraEnv(level log.Level, extraEnv []string, name string, args ...string) error {
	envCopy := append([]string(nil), extraEnv...)
	return f.recordRunCmd(true, envCopy, name, args...)
}

func (f *fakeExecutor) recordRunCmd(fromExtraEnv bool, extraEnv []string, name string, args ...string) error {
	copied := make([]string, len(args))
	copy(copied, args)
	f.runCalls = append(f.runCalls, runCmdCall{
		name:                   name,
		args:                   copied,
		fromRunCmdWithExtraEnv: fromExtraEnv,
		extraEnv:               extraEnv,
	})
	return f.runErr
}

func (f *fakeExecutor) FileExists(path string) (bool, error)                          { return false, nil }
func (f *fakeExecutor) ReadDirNames(path string) ([]string, error)                    { return nil, nil }
func (f *fakeExecutor) ReadFile(path string) ([]byte, error)                          { return nil, nil }
func (f *fakeExecutor) WriteFile(path string, content []byte, mode os.FileMode) error { return nil }
func (f *fakeExecutor) RemoveAll(path string) error                                   { return nil }
func (f *fakeExecutor) GetDistro() (*platform.Distro, error)                          { return nil, nil }
func (f *fakeExecutor) GetArchitecture() (platform.Architecture, error)               { return "", nil }
func (f *fakeExecutor) HasSudo() bool                                                 { return true }
func (f *fakeExecutor) String() string                                                { return "fake" }

func TestNewContainerEngineHonorsKindExperimentalProvider(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want Name
	}{
		{name: "docker", env: "docker", want: EngineDocker},
		{name: "podman", env: "podman", want: EnginePodman},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(kindExperimentalProviderEnv, tc.env)
			engine, err := NewContainerEngine(&fakeExecutor{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if engine.Name() != tc.want {
				t.Fatalf("got %q, want %q", engine.Name(), tc.want)
			}
		})
	}
}

func TestDockerBuildCommand(t *testing.T) {
	fx := &fakeExecutor{}
	e := NewDockerEngine(fx)

	err := e.Build(context.Background(), BuildOptions{
		ContextDir: ".",
		Dockerfile: "Dockerfile",
		Image:      "example:v1",
		Platform:   "linux/amd64",
		BuildArgs: map[string]string{
			"FOO": "bar",
		},
		ExtraArgs: []string{"--pull"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []runCmdCall{{
		name: "docker",
		args: []string{"build", "--build-arg", "FOO=bar", "--platform", "linux/amd64", "-t", "example:v1", "-f", "Dockerfile", "--pull", "."},
	}}
	if !reflect.DeepEqual(fx.runCalls, want) {
		t.Fatalf("run calls mismatch\n got: %#v\nwant: %#v", fx.runCalls, want)
	}
}

func TestPodmanBuildCommand(t *testing.T) {
	fx := &fakeExecutor{}
	e := NewPodmanEngine(fx)

	err := e.Build(context.Background(), BuildOptions{
		ContextDir: "/tmp/build",
		Dockerfile: "/tmp/build/Dockerfile",
		Image:      "example:v1",
		Platform:   "linux/arm64",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []runCmdCall{{
		name: "podman",
		args: []string{"build", "--platform", "linux/arm64", "-t", "example:v1", "-f", "/tmp/build/Dockerfile", "/tmp/build"},
	}}
	if !reflect.DeepEqual(fx.runCalls, want) {
		t.Fatalf("run calls mismatch\n got: %#v\nwant: %#v", fx.runCalls, want)
	}
}

func TestDockerTagUsesDockerCLI(t *testing.T) {
	fx := &fakeExecutor{}
	e := NewDockerEngine(fx)

	err := e.Tag(context.Background(), "src:latest", "localhost:5000/dst:latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []runCmdCall{{name: "docker", args: []string{"tag", "src:latest", "localhost:5000/dst:latest"}}}
	if !reflect.DeepEqual(fx.runCalls, want) {
		t.Fatalf("run calls mismatch\n got: %#v\nwant: %#v", fx.runCalls, want)
	}
}

func TestPodmanTagUsesPodmanCLI(t *testing.T) {
	fx := &fakeExecutor{}
	e := NewPodmanEngine(fx)

	err := e.Tag(context.Background(), "src:latest", "localhost:5000/dst:latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []runCmdCall{{name: "podman", args: []string{"tag", "src:latest", "localhost:5000/dst:latest"}}}
	if !reflect.DeepEqual(fx.runCalls, want) {
		t.Fatalf("run calls mismatch\n got: %#v\nwant: %#v", fx.runCalls, want)
	}
}

func TestPodmanPushInsecureAddsTLSVerifyFalse(t *testing.T) {
	fx := &fakeExecutor{}
	e := NewPodmanEngine(fx)

	err := e.Push(context.Background(), "localhost:5000/test:v1", PushOptions{Insecure: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []runCmdCall{{name: "podman", args: []string{"push", "--tls-verify=false", "localhost:5000/test:v1"}}}
	if !reflect.DeepEqual(fx.runCalls, want) {
		t.Fatalf("run calls mismatch\n got: %#v\nwant: %#v", fx.runCalls, want)
	}
}

func TestDockerPushInsecureAllowedRegistryPushes(t *testing.T) {
	fx := &fakeExecutor{}
	fx.execByCmd = map[string]execResult{
		"docker info --format '{{json .RegistryConfig}}'": {
			stdout: `{"IndexConfigs":{"localhost:5000":{"Secure":false}},"InsecureRegistryCIDRs":["127.0.0.0/8"]}`,
		},
	}
	e := NewDockerEngine(fx)

	err := e.Push(context.Background(), "localhost:5000/test:v1", PushOptions{Insecure: true})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	want := []runCmdCall{{name: "docker", args: []string{"push", "localhost:5000/test:v1"}}}
	if !reflect.DeepEqual(fx.runCalls, want) {
		t.Fatalf("run calls mismatch\n got: %#v\nwant: %#v", fx.runCalls, want)
	}
}

func TestDockerPushInsecureDisallowedRegistryReturnsActionableError(t *testing.T) {
	fx := &fakeExecutor{}
	fx.execByCmd = map[string]execResult{
		"docker info --format '{{json .RegistryConfig}}'": {
			stdout: `{"IndexConfigs":{"docker.io":{"Secure":true}},"InsecureRegistryCIDRs":["127.0.0.0/8"]}`,
		},
	}
	e := NewDockerEngine(fx)

	err := e.Push(context.Background(), "192.168.1.10:5000/test:v1", PushOptions{Insecure: true})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "insecure") {
		t.Fatalf("expected insecure guidance in error, got: %v", err)
	}
	if len(fx.runCalls) != 0 {
		t.Fatalf("expected no command execution for insecure docker push, got: %#v", fx.runCalls)
	}
}

func TestDockerPushInsecureDifferentPortReturnsError(t *testing.T) {
	fx := &fakeExecutor{}
	fx.execByCmd = map[string]execResult{
		"docker info --format '{{json .RegistryConfig}}'": {
			stdout: `{"IndexConfigs":{"localhost:5001":{"Secure":false}},"InsecureRegistryCIDRs":[]}`,
		},
	}
	e := NewDockerEngine(fx)

	err := e.Push(context.Background(), "localhost:5000/test:v1", PushOptions{Insecure: true})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if len(fx.runCalls) != 0 {
		t.Fatalf("expected no push execution, got: %#v", fx.runCalls)
	}
}

func TestDockerPushInsecureAllowedByCIDRPushes(t *testing.T) {
	fx := &fakeExecutor{}
	fx.execByCmd = map[string]execResult{
		"docker info --format '{{json .RegistryConfig}}'": {
			stdout: `{"IndexConfigs":{},"InsecureRegistryCIDRs":["10.96.0.0/12"]}`,
		},
	}
	e := NewDockerEngine(fx)

	err := e.Push(context.Background(), "10.96.2.10:5000/test:v1", PushOptions{Insecure: true})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	want := []runCmdCall{{name: "docker", args: []string{"push", "10.96.2.10:5000/test:v1"}}}
	if !reflect.DeepEqual(fx.runCalls, want) {
		t.Fatalf("run calls mismatch\n got: %#v\nwant: %#v", fx.runCalls, want)
	}
}

func TestRunContainerCommand(t *testing.T) {
	fx := &fakeExecutor{}
	e := NewPodmanEngine(fx)

	err := e.RunContainer(context.Background(), RunContainerOptions{
		Name:    "dpu-sim-registry",
		Image:   "registry:2",
		Detach:  true,
		Restart: "always",
		Network: "bridge",
		Publish: []string{"5001:5000"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []runCmdCall{{
		name: "podman",
		args: []string{"run", "-d", "--restart=always", "--network", "bridge", "-p", "5001:5000", "--name", "dpu-sim-registry", "registry:2"},
	}}
	if !reflect.DeepEqual(fx.runCalls, want) {
		t.Fatalf("run calls mismatch\n got: %#v\nwant: %#v", fx.runCalls, want)
	}
}

func TestInspectContainerState(t *testing.T) {
	fx := &fakeExecutor{
		execByCmd: map[string]execResult{
			"docker inspect -f '{{.State.Running}}' reg 2>/dev/null": {stdout: "true\n"},
		},
	}
	e := NewDockerEngine(fx)

	state, err := e.InspectContainerState(context.Background(), "reg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !state.Exists || !state.Running {
		t.Fatalf("unexpected state: %#v", state)
	}
}

func TestInspectContainerStateNotFoundReturnsNotExists(t *testing.T) {
	fx := &fakeExecutor{
		execByCmd: map[string]execResult{
			"podman inspect -f '{{.State.Running}}' reg 2>/dev/null": {err: fmt.Errorf("not found")},
		},
	}
	e := NewPodmanEngine(fx)

	state, err := e.InspectContainerState(context.Background(), "reg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Exists || state.Running {
		t.Fatalf("unexpected state: %#v", state)
	}
}

func TestInspectContainerNetworksParsesJSON(t *testing.T) {
	fx := &fakeExecutor{
		execByCmd: map[string]execResult{
			"podman inspect --format '{{json .NetworkSettings.Networks}}' reg": {
				stdout: `{"kind":{"IPAddress":"172.18.0.2"},"bridge":{"IPAddress":"10.88.0.4"}}`,
			},
		},
	}
	e := NewPodmanEngine(fx)

	nets, err := e.InspectContainerNetworks(context.Background(), "reg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nets["kind"].IPAddress != "172.18.0.2" {
		t.Fatalf("unexpected kind IP: %#v", nets["kind"])
	}
	if nets["bridge"].IPAddress != "10.88.0.4" {
		t.Fatalf("unexpected bridge IP: %#v", nets["bridge"])
	}
}

func TestConnectNetworkCommand(t *testing.T) {
	fx := &fakeExecutor{}
	e := NewDockerEngine(fx)

	err := e.ConnectNetwork(context.Background(), "kind", "reg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []runCmdCall{{name: "docker", args: []string{"network", "connect", "kind", "reg"}}}
	if !reflect.DeepEqual(fx.runCalls, want) {
		t.Fatalf("run calls mismatch\n got: %#v\nwant: %#v", fx.runCalls, want)
	}
}

func TestRemoveContainerForceCommand(t *testing.T) {
	fx := &fakeExecutor{}
	e := NewPodmanEngine(fx)

	err := e.RemoveContainer(context.Background(), "reg", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []runCmdCall{{name: "podman", args: []string{"rm", "-f", "reg"}}}
	if !reflect.DeepEqual(fx.runCalls, want) {
		t.Fatalf("run calls mismatch\n got: %#v\nwant: %#v", fx.runCalls, want)
	}
}
