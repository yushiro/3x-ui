package snell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CommandRunner is the host-command boundary used by prerequisite checks.
type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// CheckHost verifies the local-only requirements needed before a Snell process
// or its nftables counters can be managed. It never downloads a binary.
func CheckHost(ctx context.Context, runner CommandRunner, binaryPath, goos, goarch string) error {
	if goos != "linux" {
		return fmt.Errorf("Snell is supported only on Linux hosts")
	}
	if !supportedArchitecture(goarch) {
		return fmt.Errorf("Snell is unsupported on architecture %q", goarch)
	}
	if err := ValidateBinary(binaryPath); err != nil {
		return err
	}
	if runner == nil {
		return fmt.Errorf("Snell host command runner is unavailable")
	}

	if _, err := runner.Run(ctx, "test", "-f", "/.dockerenv"); err == nil {
		return fmt.Errorf("Snell is unsupported in Docker")
	} else if !isExitStatusOne(err) {
		return fmt.Errorf("could not verify Docker environment: %w", err)
	}
	if _, err := runner.Run(ctx, "grep", "-Eq", "(docker|containerd|kubepods)", "/proc/1/cgroup"); err == nil {
		return fmt.Errorf("Snell is unsupported in Docker")
	} else if !isExitStatusOne(err) {
		return fmt.Errorf("could not verify container environment: %w", err)
	}
	if _, err := runner.Run(ctx, "nft", "--version"); err != nil {
		return fmt.Errorf("nftables is unavailable: %w", err)
	}
	if _, err := runner.Run(ctx, "nft", "list", "ruleset"); err != nil {
		return fmt.Errorf("nftables permission check failed: %w", err)
	}
	return nil
}

// ValidateBinary requires the installer-managed Snell server binary to be a
// regular executable file; runtime code deliberately does not acquire it.
func ValidateBinary(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("Snell binary unavailable at %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("Snell binary at %q is not executable", path)
	}
	return nil
}

func supportedArchitecture(goarch string) bool {
	switch goarch {
	case "amd64", "386", "arm64", "arm":
		return true
	default:
		return false
	}
}

func isExitStatusOne(err error) bool {
	return err != nil && strings.Contains(err.Error(), "exit status 1")
}
