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
	"regexp"
	"strings"
	"time"
)

const (
	ScriptPath       = "/etc/init.d/S98tailscaled"
	ScriptBackupPath = "/kvmapp/system/init.d/S98tailscaled"
)

// UpTimeout bounds `tailscale up`, which is otherwise unbounded and runs under
// the VPN lock. Kept below the 60s browser timeout (web/src/lib/http.ts) so the
// server can answer with a real error before the client gives up, rather than
// holding the lock past the point where anyone is listening.
const UpTimeout = 45 * time.Second

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

func NewCli() *Cli {
	return &Cli{}
}

func (c *Cli) Start() error {
	for _, filePath := range []string{TailscalePath, TailscaledPath} {
		if err := utils.EnsurePermission(filePath, 0o100); err != nil {
			return err
		}
	}

	commands := []string{
		fmt.Sprintf("cp -f %s %s", ScriptBackupPath, ScriptPath),
		fmt.Sprintf("%s start", ScriptPath),
	}

	command := strings.Join(commands, " && ")
	return exec.Command("sh", "-c", command).Run()
}

func (c *Cli) Restart() error {
	commands := []string{
		fmt.Sprintf("cp -f %s %s", ScriptBackupPath, ScriptPath),
		fmt.Sprintf("%s restart", ScriptPath),
	}

	command := strings.Join(commands, " && ")
	return exec.Command("sh", "-c", command).Run()
}

func (c *Cli) Stop() error {
	// Idempotent: a client that is not installed is already stopped, and a caller
	// switching VPNs must not read that as a failure.
	//
	// The binary check is deliberately not symmetric with netbird's. S98tailscaled
	// exits 1 for every verb when /usr/sbin/tailscaled is missing — its preamble
	// runs before the case — so without this a device that never installed
	// Tailscale would report a stop failure and could never switch to NetBird.
	// The cost is real but far narrower: a daemon still running from a deleted
	// binary would be reported as stopped.
	if _, err := os.Stat(TailscaledPath); err != nil {
		return nil
	}

	if _, err := os.Stat(ScriptPath); err != nil {
		return nil
	}

	command := fmt.Sprintf("%s stop", ScriptPath)
	if err := exec.Command("sh", "-c", command).Run(); err != nil {
		return err
	}

	// Removing the script is what keeps a stopped client stopped across reboots.
	// A concurrent remover winning the race is not an error.
	if err := os.Remove(ScriptPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (c *Cli) Up() error {
	// Bounded because this runs while the VPN lock is held: `tailscale up` blocks
	// until the node authenticates, and an unauthenticated device would otherwise
	// hold the lock until the server restarts, failing every VPN request with -5.
	ctx, cancel := context.WithTimeout(context.Background(), UpTimeout)
	defer cancel()

	command := "tailscale up --accept-dns=false"
	if err := exec.CommandContext(ctx, "sh", "-c", command).Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("tailscale up timed out after %s", UpTimeout)
		}
		return err
	}

	return nil
}

func (c *Cli) Down() error {
	command := "tailscale down"
	return exec.Command("sh", "-c", command).Run()
}

func (c *Cli) Status() (*TsStatus, error) {
	command := "tailscale status --json"

	// Bounded: this now runs under the VPN lock while deciding a switch, and a
	// wedged daemon would otherwise hold that lock until the server restarts.
	ctx, cancel := context.WithTimeout(context.Background(), UpTimeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, "sh", "-c", command).CombinedOutput()
	if err != nil {
		return nil, err
	}

	// output is not in standard json format
	if outputStr := string(output); !strings.HasPrefix(outputStr, "{") {
		index := strings.Index(outputStr, "{")
		if index == -1 {
			return nil, errors.New("unknown output")
		}

		output = []byte(outputStr[index:])
	}

	var status TsStatus
	err = json.Unmarshal(output, &status)
	if err != nil {
		return nil, err
	}

	return &status, nil
}

func (c *Cli) Login() (string, error) {
	command := "tailscale login --accept-dns=false --timeout=10m"
	cmd := exec.Command("sh", "-c", command)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	defer func() {
		_ = stderr.Close()
	}()

	go func() {
		_ = cmd.Run()
	}()

	reader := bufio.NewReader(stderr)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}

		if strings.Contains(line, "https") {
			reg := regexp.MustCompile(`\s+`)
			url := reg.ReplaceAllString(line, "")
			return url, nil
		}
	}
}

func (c *Cli) Logout() error {
	command := "tailscale logout"
	return exec.Command("sh", "-c", command).Run()
}
