package snell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// ManagedProcess is the lifecycle boundary for a single Snell sidecar.
type ManagedProcess interface {
	Stop(context.Context) error
	Wait() error
	Running() bool
}

// ProcessLauncher starts one Snell sidecar against an owned config path.
type ProcessLauncher interface {
	Start(context.Context, string, string) (ManagedProcess, error)
}

type commandLauncher struct{}

// NewProcessLauncher returns the production launcher. Its only child argument
// is -c and the manager-owned configuration path.
func NewProcessLauncher() ProcessLauncher { return commandLauncher{} }

func (commandLauncher) Start(ctx context.Context, binary, configPath string) (ManagedProcess, error) {
	if binary == "" || configPath == "" {
		return nil, errors.New("Snell binary and config path are required")
	}
	// Starting is request-scoped, but the sidecar must outlive the HTTP request
	// that created its inbound. Stop is managed explicitly through Manager.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	outputBuffer := newBoundedOutput(4096)
	cmd := exec.CommandContext(context.Background(), binary, processArgs(binary, configPath)...)
	cmd.Stdout = outputBuffer
	cmd.Stderr = outputBuffer
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &commandProcess{cmd: cmd, done: make(chan struct{}), running: true, output: outputBuffer}
	go p.wait()
	return p, nil
}

func processArgs(_ string, configPath string) []string {
	return []string{"-c", configPath}
}

type commandProcess struct {
	mu      sync.RWMutex
	cmd     *exec.Cmd
	done    chan struct{}
	running bool
	err     error
	output  *boundedOutput
}

type boundedOutput struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newBoundedOutput(max int) *boundedOutput { return &boundedOutput{max: max} }

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.max <= 0 || n == 0 {
		return n, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.max {
		p = p[n-b.max:]
	}
	if len(b.buf) > b.max {
		b.buf = b.buf[:0]
	}
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		keep := len(b.buf) - b.max
		b.buf = append(b.buf[:0], b.buf[keep:]...)
	}
	return n, nil
}

func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func (p *commandProcess) wait() {
	err := p.cmd.Wait()
	p.mu.Lock()
	p.err = err
	p.running = false
	p.mu.Unlock()
	close(p.done)
}

func (p *commandProcess) Stop(ctx context.Context) error {
	p.mu.RLock()
	cmd, done, running := p.cmd, p.done, p.running
	p.mu.RUnlock()
	if !running || cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		<-done
		return ctx.Err()
	}
}

func (p *commandProcess) Wait() error {
	<-p.done
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.err
}

func (p *commandProcess) Running() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

func (p *commandProcess) LastOutput() string {
	if p.output == nil {
		return ""
	}
	return p.output.String()
}

type hostPrerequisites struct{ binaryPath string }

// NewHostChecker uses the real local command runner with the current runtime
// platform. Tests use the injected CheckHost function directly instead.
func NewHostChecker(binaryPath string) HostChecker { return hostPrerequisites{binaryPath: binaryPath} }

func (h hostPrerequisites) Check(ctx context.Context) error {
	return CheckHost(ctx, commandRunner{}, h.binaryPath, runtime.GOOS, runtime.GOARCH)
}

type commandNftExecutor struct{}

// NewNftManager returns an nft manager backed by direct, parameterized nft
// invocations. It never invokes a shell.
func NewNftManager() *NftManager { return &NftManager{Exec: commandNftExecutor{}} }

func (commandNftExecutor) Run(ctx context.Context, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, "nft", args...).CombinedOutput()
	if err != nil && len(output) > 0 {
		return output, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, err
}

var managedConfigName = regexp.MustCompile(`^snell-[1-9][0-9]*\.conf$`)

// isOwnedProcessArgs identifies only a sidecar started with this manager's
// exact binary and one of its generated config paths.
func isOwnedProcessArgs(binaryPath, configDir string, args []string) bool {
	if len(args) != 3 || filepath.Clean(args[0]) != filepath.Clean(binaryPath) {
		return false
	}
	if args[1] != "-c" && args[1] != "--config" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(configDir), filepath.Clean(args[2]))
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	return filepath.Dir(rel) == "." && managedConfigName.MatchString(filepath.Base(rel))
}

// cleanupOwnedOrphans is deliberately conservative: it only interrupts Linux
// processes whose argv exactly identifies the configured binary plus a private
// snell-N.conf path. It cannot match unrelated Snell processes or other tools.
func cleanupOwnedOrphans(binaryPath, configDir string) {
	if runtime.GOOS != "linux" {
		return
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	self := os.Getpid()
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil || len(data) == 0 {
			continue
		}
		parts := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
		if !isOwnedProcessArgs(binaryPath, configDir, parts) {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(os.Interrupt)
		}
	}
}
