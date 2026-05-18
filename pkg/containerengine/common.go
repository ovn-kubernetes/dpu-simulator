package containerengine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/platform"
)

const kindExperimentalProviderEnv = "KIND_EXPERIMENTAL_PROVIDER"

type baseEngine struct {
	name Name
	bin  string
	exec platform.CommandExecutor
}

func (e *baseEngine) Name() Name { return e.name }

func (e *baseEngine) Build(_ context.Context, opts BuildOptions) error {
	args := []string{"build"}

	if len(opts.BuildArgs) > 0 {
		keys := make([]string, 0, len(opts.BuildArgs))
		for k := range opts.BuildArgs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "--build-arg", k+"="+opts.BuildArgs[k])
		}
	}

	if opts.Platform != "" {
		args = append(args, "--platform", opts.Platform)
	}
	if opts.Image != "" {
		args = append(args, "-t", opts.Image)
	}
	if opts.Dockerfile != "" {
		args = append(args, "-f", opts.Dockerfile)
	}
	args = append(args, opts.ExtraArgs...)
	if opts.ContextDir != "" {
		args = append(args, opts.ContextDir)
	}

	return e.exec.RunCmd(log.LevelInfo, e.bin, args...)
}

func (e *baseEngine) Tag(_ context.Context, sourceRef, targetRef string) error {
	return e.exec.RunCmd(log.LevelDebug, e.bin, "tag", sourceRef, targetRef)
}

func (e *baseEngine) ImageExists(_ context.Context, imageRef string) bool {
	_, _, err := e.exec.Execute(fmt.Sprintf("%s image inspect %q >/dev/null 2>&1", e.bin, imageRef))
	return err == nil
}

func (e *baseEngine) push(_ context.Context, imageRef string, opts PushOptions, insecureFlag string) error {
	args := []string{"push"}
	if opts.Insecure && insecureFlag != "" {
		args = append(args, insecureFlag)
	}
	args = append(args, opts.ExtraArgs...)
	args = append(args, imageRef)
	return e.exec.RunCmd(log.LevelInfo, e.bin, args...)
}

func (e *baseEngine) RunContainer(_ context.Context, opts RunContainerOptions) error {
	args := []string{"run"}
	if opts.Detach {
		args = append(args, "-d")
	}
	if opts.Restart != "" {
		args = append(args, "--restart="+opts.Restart)
	}
	if opts.Network != "" {
		args = append(args, "--network", opts.Network)
	}
	for _, p := range opts.Publish {
		args = append(args, "-p", p)
	}
	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}
	args = append(args, opts.ExtraArgs...)
	args = append(args, opts.Image)

	return e.exec.RunCmd(log.LevelInfo, e.bin, args...)
}

func (e *baseEngine) RemoveContainer(_ context.Context, name string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, name)
	return e.exec.RunCmd(log.LevelDebug, e.bin, args...)
}

// TODO: Consider making this with more option other than running state, e.g. Exited, Paused, etc.
func (e *baseEngine) InspectContainerState(_ context.Context, name string) (ContainerState, error) {
	stdout, _, err := e.exec.Execute(
		fmt.Sprintf("%s inspect -f '{{.State.Running}}' %s 2>/dev/null", e.bin, name))
	if err != nil {
		return ContainerState{Exists: false, Running: false}, nil
	}
	running := strings.EqualFold(strings.TrimSpace(stdout), "true")
	return ContainerState{Exists: true, Running: running}, nil
}

func (e *baseEngine) InspectContainerNetworks(_ context.Context, name string) (map[string]NetworkEndpoint, error) {
	stdout, _, err := e.exec.Execute(
		fmt.Sprintf("%s inspect --format '{{json .NetworkSettings.Networks}}' %s", e.bin, name))
	if err != nil {
		return nil, err
	}

	var networks map[string]NetworkEndpoint
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &networks); err != nil {
		return nil, fmt.Errorf("failed to parse network inspect output: %w", err)
	}
	return networks, nil
}

func (e *baseEngine) ConnectNetwork(_ context.Context, network, container string) error {
	return e.exec.RunCmd(log.LevelDebug, e.bin, "network", "connect", network, container)
}

func NewContainerEngine(exec platform.CommandExecutor) (Engine, error) {
	detected, err := DetectName(DetectionInput{Preferred: Name(os.Getenv(kindExperimentalProviderEnv))})
	if err != nil {
		log.Warn("Invalid container engine hint: %v; falling back to auto detection", err)
		detected = EngineAuto
	}

	switch detected {
	case EngineDocker:
		log.Debug("Using Docker container engine for registry operations")
		return NewDockerEngine(exec), nil
	case EnginePodman:
		log.Debug("Using Podman container engine for registry operations")
		return NewPodmanEngine(exec), nil
	default:
		if hasEngineBinary(exec, "podman") {
			log.Debug("Auto-detected Podman container engine for registry operations")
			return NewPodmanEngine(exec), nil
		}
		log.Debug("Falling back to Docker container engine for registry operations")
		if hasEngineBinary(exec, "docker") {
			log.Debug("Auto-detected docker container engine for registry operations")
			return NewDockerEngine(exec), nil
		}
		return nil, fmt.Errorf("no container engine was found, make sure that one is available in $PATH")
	}
}

func hasEngineBinary(exec platform.CommandExecutor, bin string) bool {
	_, _, err := exec.Execute(bin + " --version")
	return err == nil
}

// NewProjectEngine returns the container engine used consistently across
// dpu-sim workflows (registry lifecycle and OVN image builds).
func NewProjectEngine(exec platform.CommandExecutor) (Engine, error) {
	engine, err := NewContainerEngine(exec)
	if err != nil {
		return nil, fmt.Errorf("error while retrieving container engine: %w", err)
	}
	return engine, nil
}
