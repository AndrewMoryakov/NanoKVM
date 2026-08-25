package netbird

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	ScriptPath       = "/etc/init.d/S99netbird"
	ScriptBackupPath = "/kvmapp/system/init.d/S99netbird"
	PidFile          = "/var/run/netbird.pid"
	CommandTimeout   = 1 * time.Minute
)

type Cli struct{}

type NbStatus struct {
	FQDN          string `json:"fqdn"`
	IP            string `json:"netbirdIp"`
	DaemonVersion string `json:"daemonVersion"`

	Management struct {
		URL       string `json:"url"`
		Connected bool   `json:"connected"`
		Error     string `json:"error"`
	} `json:"management"`

	Signal struct {
		URL       string `json:"url"`
		Connected bool   `json:"connected"`
		Error     string `json:"error"`
	} `json:"signal"`
}

func NewCli() *Cli {
	return &Cli{}
}

func (c *Cli) Start() error {
	commands := []string{
		fmt.Sprintf("cp -f %s %s", ScriptBackupPath, ScriptPath),
		fmt.Sprintf("chmod 755 %s", ScriptPath),
		fmt.Sprintf("%s start", ScriptPath),
	}
	return runCommand(strings.Join(commands, " && "), false)
}

func (c *Cli) Restart() error {
	commands := []string{
		fmt.Sprintf("cp -f %s %s", ScriptBackupPath, ScriptPath),
		fmt.Sprintf("chmod 755 %s", ScriptPath),
		fmt.Sprintf("%s restart", ScriptPath),
	}
	return runCommand(strings.Join(commands, " && "), false)
}

// Resume starts the client from the init script already on disk, without copying
// anything first. Start() begins with a cp from /kvmapp, which is exactly what
// fails when the filesystem went read-only — the case a rollback exists for.
func (c *Cli) Resume() error {
	info, err := os.Stat(ScriptPath)
	if err != nil || info.Mode()&0o111 == 0 {
		return fmt.Errorf("no usable init script at %s", ScriptPath)
	}

	return runCommand(fmt.Sprintf("%s start", ScriptPath), false)
}

func (c *Cli) Stop() error {
	// Stopping is idempotent: "no init script" and "not running" both mean the
	// client is down, which is what the caller asked about. The one real failure
	// is a daemon that outlived the signal.
	//
	// The binary gate below governs the script refresh only — stopping still runs
	// whenever a script is in place. That matters because a process outlives the
	// unlink of its own executable: a missing /usr/bin/netbird does not prove the
	// daemon is gone, and reporting success there would let a VPN switch raise a
	// second tunnel. The one path that still escapes is binary AND script both
	// gone with the daemon alive, which takes a manual rm of both over SSH.
	if _, err := os.Stat(NetbirdPath); err == nil {
		refreshScript()
	}

	// Nothing to stop if no usable script is in place. The executable bit matters:
	// sh exits 126 on a non-executable file, and ServiceRunning() reads that same
	// state as "not running" — the two must agree.
	info, err := os.Stat(ScriptPath)
	if err != nil || info.Mode()&0o111 == 0 {
		return nil
	}

	return runCommand(fmt.Sprintf("%s stop", ScriptPath), false)
}

// refreshScript copies the firmware's init script over the installed one. The
// stop semantics — polling until the process is actually gone — live in that
// script, and after an OTA the copy in /etc/init.d can predate the firmware.
// No version comparison is made: the firmware copy is always the authority.
//
// Nothing here fails the stop. A missing backup returns silently — it is a normal
// state, not an error — and a failed copy is logged. A stop error aborts a VPN
// switch, and neither condition is a reason to refuse to stop.
func refreshScript() {
	if _, err := os.Stat(ScriptBackupPath); err != nil {
		return
	}

	commands := []string{
		fmt.Sprintf("cp -f %s %s", ScriptBackupPath, ScriptPath),
		fmt.Sprintf("chmod 755 %s", ScriptPath),
	}

	if err := runCommand(strings.Join(commands, " && "), false); err != nil {
		log.Warnf("could not refresh the netbird init script, stopping with the installed one: %s", err)
	}
}

func (c *Cli) WaitForSocket(timeout time.Duration) error {
	socketPath := "/var/run/netbird.sock"
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := exec.Command("sh", "-c", "test -S "+socketPath).Output(); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("netbird daemon socket not ready after %s", timeout)
}

func (c *Cli) Login() (string, error) {
	if err := c.WaitForSocket(10 * time.Second); err != nil {
		return "", err
	}

	command := "netbird up --daemon-addr unix:///var/run/netbird.sock --no-browser"
	cmd := exec.Command("sh", "-c", command)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	if err := cmd.Start(); err != nil {
		return "", err
	}

	urlRe := regexp.MustCompile(`https://\S+`)
	urlCh := make(chan string, 1)

	scanForURL := func(r *bufio.Reader) {
		for {
			line, err := r.ReadString('\n')
			if match := urlRe.FindString(line); match != "" {
				select {
				case urlCh <- match:
				default:
				}
				return
			}
			if err != nil {
				return
			}
		}
	}

	// os/exec closes these pipes inside Wait, so Wait must not run while the
	// readers are still going: a fast-exiting command would race them and drop
	// the URL. Wait is called only after both readers have finished.
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); scanForURL(bufio.NewReader(stdout)) }()
	go func() { defer readers.Done(); scanForURL(bufio.NewReader(stderr)) }()

	done := make(chan error, 1)
	go func() {
		readers.Wait()
		done <- cmd.Wait()
	}()

	select {
	case url := <-urlCh:
		return url, nil
	case err := <-done:
		// Command finished without producing a URL — already logged in
		if err == nil {
			return "", nil
		}
		return "", fmt.Errorf("netbird up failed: %w", err)
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("timed out waiting for login URL")
	}
}

func (c *Cli) Down() error {
	return runCommand("netbird down --daemon-addr unix:///var/run/netbird.sock", true)
}

func (c *Cli) Status() (*NbStatus, error) {
	return c.status(true)
}

// StatusOnly is Status without the restart-on-timeout recovery. A caller using
// the result as a predicate needs it: recovering by restarting the service means
// the observation changes what it observes, and during a VPN switch that bounces
// the very tunnel the caller is deciding about.
func (c *Cli) StatusOnly() (*NbStatus, error) {
	return c.status(false)
}

func (c *Cli) status(restartOnTimeout bool) (*NbStatus, error) {
	output, err := runCommandWithOutput("netbird status --json --daemon-addr unix:///var/run/netbird.sock", restartOnTimeout)
	if err != nil {
		return nil, err
	}

	output = normalizeJSON(output)
	if output == "" {
		return nil, fmt.Errorf("invalid netbird status output")
	}

	var status NbStatus
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		return nil, fmt.Errorf("parse status failed: %w", err)
	}

	return &status, nil
}

func (c *Cli) ServiceRunning() (bool, error) {
	// A missing or non-executable init script means the service is not running.
	// It is a normal state: select_vpn removes the script when the other VPN is preferred.
	info, err := os.Stat(ScriptPath)
	if err != nil || info.Mode()&0o111 == 0 {
		return false, nil
	}

	output, err := runCommandWithOutput(fmt.Sprintf("%s status", ScriptPath), false)
	if err != nil {
		lower := strings.ToLower(output)
		if strings.Contains(lower, "not running") || strings.Contains(lower, "stopped") {
			return false, nil
		}
		return false, err
	}

	// Match on the negative: "not running" also contains "running".
	return !strings.Contains(strings.ToLower(output), "not running"), nil
}

func runCommand(command string, restartOnTimeout bool) error {
	_, err := runCommandWithOutput(command, restartOnTimeout)
	return err
}

func runCommandWithOutput(command string, restartOnTimeout bool) (string, error) {
	output, err, timedOut := runCommandWithTimeout(command, CommandTimeout)
	if err == nil {
		return output, nil
	}

	if !timedOut || !restartOnTimeout {
		return output, err
	}

	restartCommand := fmt.Sprintf("%s restart", ScriptPath)
	restartOutput, restartErr, _ := runCommandWithTimeout(restartCommand, CommandTimeout)
	if restartErr != nil {
		return output, fmt.Errorf("command timed out, restart failed: %w", combineCommandError(restartErr, restartOutput))
	}

	retryOutput, retryErr, _ := runCommandWithTimeout(command, CommandTimeout)
	if retryErr != nil {
		return retryOutput, fmt.Errorf("command timed out, restart succeeded, retry failed: %w", combineCommandError(retryErr, retryOutput))
	}

	return retryOutput, nil
}

func runCommandWithTimeout(command string, timeout time.Duration) (string, error, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	output, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(output))
	if err == nil {
		return msg, nil, false
	}

	if ctx.Err() == context.DeadlineExceeded {
		timeoutErr := fmt.Errorf("command timed out after %s", timeout)
		return msg, combineCommandError(timeoutErr, msg), true
	}

	return msg, combineCommandError(err, msg), false
}

func combineCommandError(err error, output string) error {
	msg := strings.TrimSpace(output)
	if msg == "" {
		return err
	}

	return fmt.Errorf("%w: %s", err, msg)
}

func normalizeJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") {
		return s
	}

	index := strings.Index(s, "{")
	if index == -1 {
		return ""
	}

	return strings.TrimSpace(s[index:])
}
