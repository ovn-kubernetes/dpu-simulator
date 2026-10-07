// Package platform provides utilities for Linux distribution detection and management
package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ovn-kubernetes/dpu-simulator/pkg/config"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/log"
	"github.com/ovn-kubernetes/dpu-simulator/pkg/ssh"
)

// CommandExecutor is an interface for executing commands on different platforms.
// Implementations allow the same installation logic to work across:
// - Local execution (host machine)
// - SSH execution (remote VMs/baremetal)
// - Docker execution (Kind containers)
type CommandExecutor interface {
	// WaitUntilReady waits until the executor is ready to execute commands
	WaitUntilReady(timeout time.Duration) error

	// Execute runs a command and returns stdout, stderr, and error
	Execute(command string) (stdout, stderr string, err error)

	// ExecuteWithTimeout runs a command with a specific timeout
	ExecuteWithTimeout(command string, timeout time.Duration) (stdout, stderr string, err error)

	// ExecuteRetryWithTimeout retries a command at the given interval until it
	// succeeds or the overall timeout expires. Useful for waiting on conditions.
	ExecuteRetryWithTimeout(command string, interval, timeout time.Duration) (stdout, stderr string, err error)

	// RunCmd executes a command with arguments
	// The level parameter controls output visibility:
	// - If level <= global log level: output streams to stdout/stderr
	// - If level > global log level: output is captured silently (included in error on failure)
	RunCmd(level log.Level, name string, args ...string) error

	// RunCmdInDir executes a command with arguments in a specific working directory.
	// Behaves like RunCmd but sets the working directory before execution.
	RunCmdInDir(level log.Level, dir string, name string, args ...string) error

	// RunCmdWithExtraEnv runs a command with the executor's usual subprocess environment
	// plus extraEnv (each element "KEY=value"). When extraEnv is empty, behavior matches RunCmd.
	RunCmdWithExtraEnv(level log.Level, extraEnv []string, name string, args ...string) error

	// FileExists checks if a file or directory exists on the target system
	FileExists(path string) (bool, error)

	// ReadDirNames returns base names of entries in a directory (like os.ReadDir).
	// The path must exist and refer to a directory.
	ReadDirNames(path string) ([]string, error)

	// ReadFile reads the contents of a file on the target system
	ReadFile(path string) ([]byte, error)

	// WriteFile writes content to a file on the target system
	WriteFile(path string, content []byte, mode os.FileMode) error

	// RemoveAll removes a path and any children it contains on the target system
	RemoveAll(path string) error

	// GetDistro returns the Linux distribution information for this executor's target
	// The result is cached after the first probe.
	GetDistro() (*Distro, error)

	// GetArchitecture returns the architecture of the target system
	GetArchitecture() (Architecture, error)

	// HasSudo returns true if "sudo" is available on the target system.
	// The result is cached after the first probe.
	HasSudo() bool

	// String returns a description of this executor (for logging)
	String() string
}

// ShQuote returns s wrapped in single quotes for use in POSIX shell commands,
// with any embedded single quotes escaped for sh -c.
func ShQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func parseReadDirLines(stdout string) []string {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return nil
	}
	lines := strings.Split(stdout, "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names
}

// MeaningfulDirEntries returns directory entry names excluding ".git", so a tree
// that only contains ".git" is treated as empty for re-clone / cleanup decisions.
func MeaningfulDirEntries(names []string) []string {
	var out []string
	for _, n := range names {
		if n != ".git" {
			out = append(out, n)
		}
	}
	return out
}

// RunCommandInDir runs name with args in the executor's shell via ExecuteWithTimeout,
// optionally prefixing with cd dir when dir is non-empty.
func RunCommandInDir(
	cmdExec CommandExecutor,
	dir string,
	name string,
	args []string,
	timeout time.Duration,
) (stdout, stderr string, err error) {
	var sb strings.Builder
	if dir != "" {
		sb.WriteString("cd ")
		sb.WriteString(ShQuote(dir))
		sb.WriteString(" && ")
	}
	sb.WriteString(ShQuote(name))
	for _, a := range args {
		sb.WriteString(" ")
		sb.WriteString(ShQuote(a))
	}
	return cmdExec.ExecuteWithTimeout(sb.String(), timeout)
}

// CombinedCmdOutput joins stdout and stderr similarly to exec.Cmd.CombinedOutput for logs and errors.
func CombinedCmdOutput(stdout, stderr string) string {
	if stderr == "" {
		return stdout
	}
	if stdout == "" {
		return stderr
	}
	return stdout + "\n" + stderr
}

// stripSudoCmd removes "sudo" from RunCmd-style arguments when sudo is
// unavailable.
func stripSudoCmd(hasSudo bool, name string, args []string) (string, []string) {
	if !hasSudo && name == "sudo" && len(args) > 0 {
		return args[0], args[1:]
	}
	return name, args
}

// stripSudoScript removes "sudo " prefixes from shell script strings when
// sudo is unavailable. Handles both line-start and mid-line occurrences
// (e.g. after "&&", pipes, or semicolons).
func stripSudoScript(hasSudo bool, command string) string {
	if hasSudo {
		return command
	}
	result := strings.ReplaceAll(command, "sudo ", "")
	return result
}

// rawExecute runs a command without any sudo stripping. Used internally only
// by probeSudo to avoid infinite recursion.
type rawExecutor interface {
	rawExecuteWithTimeout(command string, timeout time.Duration) (stdout, stderr string, err error)
}

// executeRetryWithTimeout is the shared implementation for
// CommandExecutor.ExecuteRetryWithTimeout.
func executeRetryWithTimeout(exec CommandExecutor, command string, interval, timeout time.Duration) (string, string, error) {
	deadline := time.Now().Add(timeout)
	var stdout, stderr string
	var err error
	for {
		stdout, stderr, err = exec.ExecuteWithTimeout(command, interval)
		if err == nil {
			return stdout, stderr, nil
		}
		if time.Now().Add(interval).After(deadline) {
			return stdout, stderr, fmt.Errorf("timed out after %s: %w", timeout, err)
		}
		time.Sleep(interval)
	}
}

// probeSudo checks whether "sudo" is available on the target system.
func probeSudo(cmdExec rawExecutor) bool {
	_, _, err := cmdExec.rawExecuteWithTimeout("which sudo", 5*time.Second)
	return err == nil
}

// LocalExecutor executes commands on the local machine
type LocalExecutor struct {
	cachedDistro *Distro
	cachedSudo   *bool
}

// NewLocalExecutor creates a new LocalExecutor
func NewLocalExecutor() *LocalExecutor {
	return &LocalExecutor{}
}

// WaitUntilReady does nothing for local execution
func (e *LocalExecutor) WaitUntilReady(timeout time.Duration) error {
	return nil
}

// HasSudo returns true if "sudo" is available locally.
func (e *LocalExecutor) HasSudo() bool {
	if e.cachedSudo != nil {
		return *e.cachedSudo
	}
	v := probeSudo(e)
	e.cachedSudo = &v
	return v
}

// Execute runs a command locally
func (e *LocalExecutor) Execute(command string) (stdout, stderr string, err error) {
	return e.ExecuteWithTimeout(command, 30*time.Second)
}

func (e *LocalExecutor) rawExecuteWithTimeout(command string, timeout time.Duration) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

// ExecuteWithTimeout runs a command with a specific timeout
func (e *LocalExecutor) ExecuteWithTimeout(command string, timeout time.Duration) (stdout, stderr string, err error) {
	return e.rawExecuteWithTimeout(stripSudoScript(e.HasSudo(), command), timeout)
}

// ExecuteRetryWithTimeout retries a command until it succeeds or times out.
func (e *LocalExecutor) ExecuteRetryWithTimeout(command string, interval, timeout time.Duration) (string, string, error) {
	return executeRetryWithTimeout(e, command, interval, timeout)
}

// RunCmd executes a command with arguments
func (e *LocalExecutor) RunCmd(level log.Level, name string, args ...string) error {
	name, args = stripSudoCmd(e.HasSudo(), name, args)
	cmd := exec.Command(name, args...)

	// If the requested level is visible, stream to stdout/stderr/stdin
	if level <= log.GetLevel() {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Otherwise capture output silently
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("command failed: %w\nstdout: %s\nstderr: %s", err, stdoutBuf.String(), stderrBuf.String())
	}
	return nil
}

// RunCmdInDir executes a command with arguments in a specific working directory
func (e *LocalExecutor) RunCmdInDir(level log.Level, dir string, name string, args ...string) error {
	name, args = stripSudoCmd(e.HasSudo(), name, args)
	cmd := exec.Command(name, args...)
	cmd.Dir = dir

	// If the requested level is visible, stream to stdout/stderr/stdin
	if level <= log.GetLevel() {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Otherwise capture output silently
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("command failed: %w\nstdout: %s\nstderr: %s", err, stdoutBuf.String(), stderrBuf.String())
	}
	return nil
}

// RunCmdWithExtraEnv runs a command locally with optional extra environment variables.
func (e *LocalExecutor) RunCmdWithExtraEnv(level log.Level, extraEnv []string, name string, args ...string) error {
	name, args = stripSudoCmd(e.HasSudo(), name, args)
	cmd := exec.Command(name, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}

	if level <= log.GetLevel() {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("command failed: %w\nstdout: %s\nstderr: %s", err, stdoutBuf.String(), stderrBuf.String())
	}
	return nil
}

// FileExists checks if a file or directory exists on the local system
func (e *LocalExecutor) FileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// ReadDirNames returns directory entry names on the local system.
func (e *LocalExecutor) ReadDirNames(path string) ([]string, error) {
	ents, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ents))
	for _, ent := range ents {
		names = append(names, ent.Name())
	}
	return names, nil
}

// ReadFile reads the contents of a file on the local system
func (e *LocalExecutor) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// WriteFile writes content to a file on the local system
func (e *LocalExecutor) WriteFile(path string, content []byte, mode os.FileMode) error {
	return os.WriteFile(path, content, mode)
}

// RemoveAll removes a path and any children it contains on the local system
func (e *LocalExecutor) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

// GetDistro returns the Linux distribution information for the local machine
func (e *LocalExecutor) GetDistro() (*Distro, error) {
	if e.cachedDistro != nil {
		return e.cachedDistro, nil
	}
	distro, err := DetectDistro(e)
	if err != nil {
		return nil, err
	}
	e.cachedDistro = distro
	return distro, nil
}

// GetArchitecture returns the architecture of the local system
func (e *LocalExecutor) GetArchitecture() (Architecture, error) {
	return DetectArchitecture(e)
}

// String returns a description of this executor
func (e *LocalExecutor) String() string {
	return "local"
}

// SSHExecutor executes commands on a remote machine via SSH
type SSHExecutor struct {
	client       *ssh.SSHClient
	config       *config.SSHConfig
	ip           string
	cachedDistro *Distro
	cachedSudo   *bool
}

// NewSSHExecutor creates a new SSHExecutor for a specific remote host
func NewSSHExecutor(cfg *config.SSHConfig, ip string) *SSHExecutor {
	return &SSHExecutor{
		client: ssh.NewSSHClient(cfg),
		config: cfg,
		ip:     ip,
	}
}

// WaitUntilReady waits until the SSH executor is ready to execute commands
func (e *SSHExecutor) WaitUntilReady(timeout time.Duration) error {
	return e.client.WaitForSSH(e.ip, timeout)
}

// HasSudo returns true if "sudo" is available on the remote host.
func (e *SSHExecutor) HasSudo() bool {
	if e.cachedSudo != nil {
		return *e.cachedSudo
	}
	v := probeSudo(e)
	e.cachedSudo = &v
	return v
}

// Execute runs a command on the remote host
func (e *SSHExecutor) Execute(command string) (stdout, stderr string, err error) {
	return e.ExecuteWithTimeout(command, 30*time.Second)
}

func (e *SSHExecutor) rawExecuteWithTimeout(command string, timeout time.Duration) (stdout, stderr string, err error) {
	return e.client.ExecuteWithTimeout(e.ip, command, timeout)
}

// ExecuteWithTimeout runs a command with a specific timeout
func (e *SSHExecutor) ExecuteWithTimeout(command string, timeout time.Duration) (stdout, stderr string, err error) {
	return e.rawExecuteWithTimeout(stripSudoScript(e.HasSudo(), command), timeout)
}

// ExecuteRetryWithTimeout retries a command until it succeeds or times out.
func (e *SSHExecutor) ExecuteRetryWithTimeout(command string, interval, timeout time.Duration) (string, string, error) {
	return executeRetryWithTimeout(e, command, interval, timeout)
}

// RunCmd executes a command with arguments
func (e *SSHExecutor) RunCmd(level log.Level, name string, args ...string) error {
	name, args = stripSudoCmd(e.HasSudo(), name, args)
	command := name
	for _, arg := range args {
		// Escape single quotes in arguments
		escaped := strings.ReplaceAll(arg, "'", "'\"'\"'")
		command += " '" + escaped + "'"
	}

	stdout, stderr, err := e.ExecuteWithTimeout(command, 5*time.Minute)

	// If the requested level is visible, print output
	if level <= log.GetLevel() {
		if stdout != "" {
			fmt.Print(stdout)
		}
		if stderr != "" {
			fmt.Fprint(os.Stderr, stderr)
		}
		return err
	}

	// Otherwise only include output in error
	if err != nil {
		return fmt.Errorf("command failed: %w\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	return nil
}

// RunCmdInDir executes a command with arguments in a specific working directory via SSH
func (e *SSHExecutor) RunCmdInDir(level log.Level, dir string, name string, args ...string) error {
	name, args = stripSudoCmd(e.HasSudo(), name, args)
	command := "cd " + ShQuote(dir) + " && " + ShQuote(name)
	for _, arg := range args {
		command += " " + ShQuote(arg)
	}

	stdout, stderr, err := e.ExecuteWithTimeout(command, 5*time.Minute)

	// If the requested level is visible, print output
	if level <= log.GetLevel() {
		if stdout != "" {
			fmt.Print(stdout)
		}
		if stderr != "" {
			fmt.Fprint(os.Stderr, stderr)
		}
		return err
	}

	// Otherwise only include output in error
	if err != nil {
		return fmt.Errorf("command failed: %w\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	return nil
}

// RunCmdWithExtraEnv runs a command on the remote host with extra environment variables.
func (e *SSHExecutor) RunCmdWithExtraEnv(level log.Level, extraEnv []string, name string, args ...string) error {
	if len(extraEnv) == 0 {
		return e.RunCmd(level, name, args...)
	}
	name, args = stripSudoCmd(e.HasSudo(), name, args)
	command := "env "
	for _, kv := range extraEnv {
		command += ShQuote(kv) + " "
	}
	command += ShQuote(name)
	for _, arg := range args {
		command += " " + ShQuote(arg)
	}

	stdout, stderr, err := e.ExecuteWithTimeout(command, 5*time.Minute)

	if level <= log.GetLevel() {
		if stdout != "" {
			fmt.Print(stdout)
		}
		if stderr != "" {
			fmt.Fprint(os.Stderr, stderr)
		}
		return err
	}

	if err != nil {
		return fmt.Errorf("command failed: %w\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	return nil
}

// FileExists checks if a file or directory exists on the remote system
func (e *SSHExecutor) FileExists(path string) (bool, error) {
	_, _, err := e.Execute(fmt.Sprintf("test -e '%s'", path))
	if err != nil {
		return false, nil
	}
	return true, nil
}

// ReadDirNames lists directory entries on the remote host via ls -1A.
func (e *SSHExecutor) ReadDirNames(path string) ([]string, error) {
	cmd := fmt.Sprintf("ls -1A %s", ShQuote(path))
	stdout, stderr, err := e.ExecuteWithTimeout(cmd, 2*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("list directory %s: %w\nstderr: %s", path, err, strings.TrimSpace(stderr))
	}
	return parseReadDirLines(stdout), nil
}

// ReadFile reads the contents of a file on the remote system
func (e *SSHExecutor) ReadFile(path string) ([]byte, error) {
	stdout, _, err := e.Execute(fmt.Sprintf("cat '%s'", path))
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s via SSH: %w", path, err)
	}
	return []byte(stdout), nil
}

// WriteFile writes content to a file on the remote system
func (e *SSHExecutor) WriteFile(path string, content []byte, mode os.FileMode) error {
	command := sshWriteFileCommand(path, content, mode)

	_, _, err := e.ExecuteWithTimeout(command, 30*time.Second)
	if err != nil {
		return fmt.Errorf("failed to write file via SSH: %w", err)
	}

	return nil
}

// Encode the payload so shell syntax, heredoc markers, NULs, and trailing
// newlines are preserved exactly. Only the destination path needs shell quoting.
func sshWriteFileCommand(path string, content []byte, mode os.FileMode) string {
	return fmt.Sprintf("printf %%s %s | base64 -d > %s && chmod %o %s",
		ShQuote(base64.StdEncoding.EncodeToString(content)), ShQuote(path), mode.Perm(), ShQuote(path))
}

// RemoveAll removes a path and any children it contains on the remote system
func (e *SSHExecutor) RemoveAll(path string) error {
	_, _, err := e.Execute(fmt.Sprintf("rm -rf '%s'", path))
	return err
}

// GetDistro returns the Linux distribution information for the remote machine
func (e *SSHExecutor) GetDistro() (*Distro, error) {
	if e.cachedDistro != nil {
		return e.cachedDistro, nil
	}
	distro, err := DetectDistro(e)
	if err != nil {
		return nil, err
	}
	e.cachedDistro = distro
	return distro, nil
}

// GetArchitecture returns the architecture of the remote system
func (e *SSHExecutor) GetArchitecture() (Architecture, error) {
	return DetectArchitecture(e)
}

// String returns a description of this executor
func (e *SSHExecutor) String() string {
	return fmt.Sprintf("ssh://%s@%s", e.config.User, e.ip)
}

// DockerExecutor executes commands inside a container via docker or podman
type DockerExecutor struct {
	containerID  string
	containerBin string // "docker" or "podman"
	cachedDistro *Distro
	cachedSudo   *bool
}

// NewDockerExecutor creates a new DockerExecutor for a specific container.
// containerBin selects the container runtime ("docker" or "podman").
func NewDockerExecutor(containerID, containerBin string) *DockerExecutor {
	return &DockerExecutor{
		containerID:  containerID,
		containerBin: containerBin,
	}
}

// WaitUntilReady waits until the Docker executor is ready to execute commands
func (e *DockerExecutor) WaitUntilReady(timeout time.Duration) error {
	return nil
}

// HasSudo returns true if "sudo" is available inside the container.
func (e *DockerExecutor) HasSudo() bool {
	if e.cachedSudo != nil {
		return *e.cachedSudo
	}
	v := probeSudo(e)
	e.cachedSudo = &v
	return v
}

// Execute runs a command inside the container
func (e *DockerExecutor) Execute(command string) (stdout, stderr string, err error) {
	return e.ExecuteWithTimeout(command, 30*time.Second)
}

func (e *DockerExecutor) rawExecuteWithTimeout(command string, timeout time.Duration) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.containerBin, "exec", e.containerID, "sh", "-c", command)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

// ExecuteWithTimeout runs a command with a specific timeout
func (e *DockerExecutor) ExecuteWithTimeout(command string, timeout time.Duration) (stdout, stderr string, err error) {
	return e.rawExecuteWithTimeout(stripSudoScript(e.HasSudo(), command), timeout)
}

// ExecuteRetryWithTimeout retries a command until it succeeds or times out.
func (e *DockerExecutor) ExecuteRetryWithTimeout(command string, interval, timeout time.Duration) (string, string, error) {
	return executeRetryWithTimeout(e, command, interval, timeout)
}

// RunCmd executes a command with arguments inside the container
func (e *DockerExecutor) RunCmd(level log.Level, name string, args ...string) error {
	name, args = stripSudoCmd(e.HasSudo(), name, args)
	dockerArgs := []string{"exec", e.containerID, name}
	dockerArgs = append(dockerArgs, args...)

	cmd := exec.Command(e.containerBin, dockerArgs...)

	// If the requested level is visible, stream to stdout/stderr
	if level <= log.GetLevel() {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Otherwise capture output silently
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("command failed: %w\nstdout: %s\nstderr: %s", err, stdoutBuf.String(), stderrBuf.String())
	}
	return nil
}

// RunCmdInDir executes a command with arguments in a specific working directory inside the container
func (e *DockerExecutor) RunCmdInDir(level log.Level, dir string, name string, args ...string) error {
	name, args = stripSudoCmd(e.HasSudo(), name, args)
	var sb strings.Builder
	sb.WriteString("cd ")
	sb.WriteString(ShQuote(dir))
	sb.WriteString(" && ")
	sb.WriteString(ShQuote(name))
	for _, a := range args {
		sb.WriteString(" ")
		sb.WriteString(ShQuote(a))
	}
	command := sb.String()

	dockerArgs := []string{"exec", e.containerID, "sh", "-c", command}
	cmd := exec.Command(e.containerBin, dockerArgs...)

	// If the requested level is visible, stream to stdout/stderr
	if level <= log.GetLevel() {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Otherwise capture output silently
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("command failed: %w\nstdout: %s\nstderr: %s", err, stdoutBuf.String(), stderrBuf.String())
	}
	return nil
}

// RunCmdWithExtraEnv runs a command inside the container with extra environment variables.
func (e *DockerExecutor) RunCmdWithExtraEnv(level log.Level, extraEnv []string, name string, args ...string) error {
	name, args = stripSudoCmd(e.HasSudo(), name, args)
	dockerArgs := []string{"exec"}
	for _, kv := range extraEnv {
		dockerArgs = append(dockerArgs, "-e", kv)
	}
	dockerArgs = append(dockerArgs, e.containerID, name)
	dockerArgs = append(dockerArgs, args...)

	cmd := exec.Command(e.containerBin, dockerArgs...)

	if level <= log.GetLevel() {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("command failed: %w\nstdout: %s\nstderr: %s", err, stdoutBuf.String(), stderrBuf.String())
	}
	return nil
}

// FileExists checks if a file or directory exists inside the container
func (e *DockerExecutor) FileExists(path string) (bool, error) {
	_, _, err := e.Execute(fmt.Sprintf("test -e '%s'", path))
	if err != nil {
		return false, nil
	}
	return true, nil
}

// ReadDirNames lists directory entries inside the container via ls -1A.
func (e *DockerExecutor) ReadDirNames(path string) ([]string, error) {
	cmd := fmt.Sprintf("ls -1A %s", ShQuote(path))
	stdout, stderr, err := e.ExecuteWithTimeout(cmd, 2*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("list directory %s in container: %w\nstderr: %s", path, err, strings.TrimSpace(stderr))
	}
	return parseReadDirLines(stdout), nil
}

// ReadFile reads the contents of a file inside the container
func (e *DockerExecutor) ReadFile(path string) ([]byte, error) {
	stdout, _, err := e.Execute(fmt.Sprintf("cat '%s'", path))
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s in container: %w", path, err)
	}
	return []byte(stdout), nil
}

// WriteFile writes content to a file inside the container
func (e *DockerExecutor) WriteFile(path string, content []byte, mode os.FileMode) error {
	// Use docker cp via stdin
	cmd := exec.Command(e.containerBin, "exec", "-i", e.containerID, "sh", "-c",
		fmt.Sprintf("cat > '%s' && chmod %o '%s'", path, mode, path))

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start docker exec: %w", err)
	}

	if _, err := io.Copy(stdin, bytes.NewReader(content)); err != nil {
		return fmt.Errorf("failed to write content: %w", err)
	}
	stdin.Close()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("docker exec failed: %w", err)
	}

	return nil
}

// RemoveAll removes a path and any children it contains inside the container
func (e *DockerExecutor) RemoveAll(path string) error {
	_, _, err := e.Execute(fmt.Sprintf("rm -rf '%s'", path))
	return err
}

// GetDistro returns the Linux distribution information for the container
func (e *DockerExecutor) GetDistro() (*Distro, error) {
	if e.cachedDistro != nil {
		return e.cachedDistro, nil
	}
	distro, err := DetectDistro(e)
	if err != nil {
		return nil, err
	}
	e.cachedDistro = distro
	return distro, nil
}

// GetArchitecture returns the architecture of the container
func (e *DockerExecutor) GetArchitecture() (Architecture, error) {
	return DetectArchitecture(e)
}

// String returns a description of this executor
func (e *DockerExecutor) String() string {
	return fmt.Sprintf("%s://%s", e.containerBin, e.containerID)
}
