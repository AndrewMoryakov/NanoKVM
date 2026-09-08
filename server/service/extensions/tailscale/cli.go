package tailscale

import (
	"NanoKVM-Server/utils"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	ScriptPath       = "/etc/init.d/S98tailscaled"
	ScriptBackupPath = "/kvmapp/system/init.d/S98tailscaled"
)

// All synchronous lifecycle calls must finish before the browser's normal
// request timeout. Login is intentionally different: it is an interactive,
// long-running operation and is owned/cancelled separately below.
const (
	UpTimeout        = 45 * time.Second
	DownTimeout      = 45 * time.Second
	StatusTimeout    = 10 * time.Second
	ScriptTimeout    = 45 * time.Second
	LoginURLTimeout  = 60 * time.Second
	LoginStopTimeout = 5 * time.Second
)

type Cli struct{}

type TsStatus struct {
	BackendState string `json:"BackendState"`

	Self struct {
		HostName     string   `json:"HostName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`

	CurrentTailnet struct {
		Name string `json:"Name"`
	} `json:"CurrentTailnet"`
}

// An interactive `tailscale login` must not be detached from the server. The
// HTTP request returns as soon as it gets the URL, but a subsequent Stop, Down,
// Logout, Restart or Uninstall has to cancel the still-running CLI before it
// changes daemon state. Only one login may own the client at a time.
type loginProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
}

var activeLogin struct {
	sync.Mutex
	process *loginProcess
}

var loginURLRE = regexp.MustCompile(`https://\S+`)

func NewCli() *Cli {
	return &Cli{}
}

func (c *Cli) Start() error {
	for _, filePath := range []string{TailscalePath, TailscaledPath} {
		if err := utils.EnsurePermission(filePath, 0o100); err != nil {
			return err
		}
	}

	if err := installInitScript(); err != nil {
		return err
	}
	return runProgram(ScriptTimeout, ScriptPath, "start")
}

func (c *Cli) Restart() error {
	if err := cancelLogin(); err != nil {
		return fmt.Errorf("cancel active tailscale login: %w", err)
	}
	if err := installInitScript(); err != nil {
		return err
	}
	return runProgram(ScriptTimeout, ScriptPath, "restart")
}

// Resume starts the client from the init script already on disk. It is used by
// SetPreference rollback and deliberately does not copy the firmware script:
// a read-only filesystem must make the rollback fail explicitly, rather than
// silently changing the boot configuration.
func (c *Cli) Resume() error {
	if !isExecutable(ScriptPath) {
		return fmt.Errorf("no init script at %s to resume from", ScriptPath)
	}
	return runProgram(UpTimeout, ScriptPath, "start")
}

// StopRuntime stops the daemon but preserves S98. Switching VPNs uses this
// operation so a failed atomic preference write can resume the old daemon.
func (c *Cli) StopRuntime() error {
	if err := cancelLogin(); err != nil {
		return fmt.Errorf("cancel active tailscale login: %w", err)
	}

	script, err := stopScript("tailscaled", TailscaledPath)
	if err != nil || script == "" {
		return err
	}
	return runProgram(ScriptTimeout, script, "stop")
}

// Stop is the explicit UI action that also disables Tailscale autostart. It
// only removes S98 after StopRuntime has confirmed that the script succeeded.
func (c *Cli) Stop() error {
	if err := c.StopRuntime(); err != nil {
		return err
	}
	if err := os.Remove(ScriptPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove tailscale init script: %w", err)
	}
	return nil
}

func (c *Cli) Up() error {
	if err := runProgram(UpTimeout, TailscalePath, "up", "--accept-dns=false"); err != nil {
		return fmt.Errorf("tailscale up: %w", err)
	}
	return nil
}

func (c *Cli) Down() error {
	if err := cancelLogin(); err != nil {
		return fmt.Errorf("cancel active tailscale login: %w", err)
	}
	return runProgram(DownTimeout, TailscalePath, "down")
}

func (c *Cli) Status() (*TsStatus, error) {
	output, err := runProgramOutput(StatusTimeout, TailscalePath, "status", "--json")
	if err != nil {
		return nil, err
	}

	// Some old CLI versions prefix JSON with a warning.
	if outputStr := strings.TrimSpace(output); !strings.HasPrefix(outputStr, "{") {
		index := strings.Index(outputStr, "{")
		if index == -1 {
			return nil, errors.New("unknown tailscale status output")
		}
		output = outputStr[index:]
	}

	var status TsStatus
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *Cli) Login() (string, error) {
	cmd := exec.Command(TailscalePath, "login", "--accept-dns=false", "--timeout=10m")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	process, err := startLogin(cmd)
	if err != nil {
		return "", err
	}

	urlCh := make(chan string, 1)
	var readers sync.WaitGroup
	readers.Add(2)
	go scanLoginURL(bufio.NewReader(stdout), urlCh, &readers)
	go scanLoginURL(bufio.NewReader(stderr), urlCh, &readers)

	result := make(chan error, 1)
	go func() {
		readers.Wait()
		err := cmd.Wait()
		finishLogin(process)
		result <- err
		close(process.done)
	}()

	select {
	case url := <-urlCh:
		return url, nil
	case err := <-result:
		if err == nil {
			return "", nil // already authenticated
		}
		return "", fmt.Errorf("tailscale login failed: %w", err)
	case <-time.After(LoginURLTimeout):
		_ = stopLogin(process)
		return "", fmt.Errorf("timed out waiting for tailscale login URL")
	}
}

func (c *Cli) Logout() error {
	if err := cancelLogin(); err != nil {
		return fmt.Errorf("cancel active tailscale login: %w", err)
	}
	return runProgram(DownTimeout, TailscalePath, "logout")
}

func installInitScript() error {
	contents, err := os.ReadFile(ScriptBackupPath)
	if err != nil {
		return fmt.Errorf("read tailscale init script: %w", err)
	}
	return replaceInitScript(ScriptPath, contents, "tailscale")
}

// replaceInitScript avoids leaving a truncated S98 after an interrupted copy.
// The init script is the recovery path for a remote device, so replacing it is
// a small filesystem transaction just like the VPN preference file.
func replaceInitScript(path string, contents []byte, client string) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+client+".init-")
	if err != nil {
		return fmt.Errorf("create %s init script: %w", client, err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if err := temporary.Chmod(0o755); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("chmod %s init script: %w", client, err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s init script: %w", client, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync %s init script: %w", client, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s init script: %w", client, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %s init script: %w", client, err)
	}
	if dir, err := os.Open(directory); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// stopScript returns a usable stop script even when the installed init script
// was deleted. The firmware backup is executable in place and does not restore
// S98, preserving the distinction between stopping runtime and autostart.
func stopScript(processName, executable string) (string, error) {
	for _, script := range []string{ScriptBackupPath, ScriptPath} {
		if isExecutable(script) {
			return script, nil
		}
	}

	running, err := daemonPresent(processName, executable)
	if err != nil {
		return "", err
	}
	if running {
		return "", fmt.Errorf("%s is running but no usable init script is available to stop it", processName)
	}
	return "", nil
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// daemonPresentForService is a narrow seam for the daemon-identity probe.  It
// keeps ServiceRunning deterministic to test without weakening the production
// check, which remains daemonPresent's pidof + /proc/<pid>/exe verification.
var daemonPresentForService = daemonPresent

// ServiceRunning reports whether a real tailscaled daemon is present. It does
// not use `tailscale status`: that command can fail while the daemon remains
// alive (for example while its local socket is transiently unavailable).
//
// The result is based on the same /proc executable identity check used by the
// stop/uninstall recovery path, so a stale PID or unrelated process cannot be
// mistaken for tailscaled.
func (c *Cli) ServiceRunning() (bool, error) {
	return daemonPresentForService("tailscaled", TailscaledPath)
}

// daemonPresent verifies the executable via /proc rather than trusting a stale
// pid file or a process with the same name. A removed executable remains
// visible as "<path> (deleted)", which still has to block unsafe removal.
func daemonPresent(name, executable string) (bool, error) {
	output, err := exec.Command("pidof", name).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("find %s process: %w", name, err)
	}
	for _, pid := range strings.Fields(string(output)) {
		target, err := os.Readlink("/proc/" + pid + "/exe")
		if err != nil {
			continue // exited between pidof and inspection
		}
		if target == executable || target == executable+" (deleted)" {
			return true, nil
		}
	}
	return false, nil
}

func runProgram(timeout time.Duration, name string, args ...string) error {
	_, err := runProgramOutput(timeout, name, args...)
	return err
}

func runProgramOutput(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	text := strings.TrimSpace(string(output))
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("%s timed out after %s: %w", name, timeout, context.DeadlineExceeded)
	}
	if err != nil {
		if text == "" {
			return text, err
		}
		return text, fmt.Errorf("%w: %s", err, text)
	}
	return text, nil
}

func startLogin(cmd *exec.Cmd) (*loginProcess, error) {
	activeLogin.Lock()
	defer activeLogin.Unlock()
	if activeLogin.process != nil {
		return nil, errors.New("tailscale login is already in progress")
	}
	configureLoginCommand(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	process := &loginProcess{cmd: cmd, done: make(chan struct{})}
	activeLogin.process = process
	return process, nil
}

func finishLogin(process *loginProcess) {
	activeLogin.Lock()
	if activeLogin.process == process {
		activeLogin.process = nil
	}
	activeLogin.Unlock()
}

func cancelLogin() error {
	activeLogin.Lock()
	process := activeLogin.process
	activeLogin.Unlock()
	if process == nil {
		return nil
	}
	return stopLogin(process)
}

func stopLogin(process *loginProcess) error {
	if err := killLoginCommand(process.cmd); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-process.done:
		return nil
	case <-time.After(LoginStopTimeout):
		return fmt.Errorf("tailscale login did not terminate within %s", LoginStopTimeout)
	}
}

func scanLoginURL(reader *bufio.Reader, urls chan<- string, wg *sync.WaitGroup) {
	defer wg.Done()
	sent := false
	for {
		line, err := reader.ReadString('\n')
		if !sent {
			if url := loginURLRE.FindString(line); url != "" {
				select {
				case urls <- url:
				default:
				}
				sent = true
			}
		}
		if err != nil {
			return
		}
	}
}
