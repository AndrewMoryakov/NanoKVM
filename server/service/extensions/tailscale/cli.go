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

// CommandTimeout bounds every init-script call. S98tailscaled sleeps 5 seconds
// on start and then runs `tailscale set`, so without a deadline a wedged daemon
// leaves the HTTP handler hanging past any client timeout.
const CommandTimeout = 1 * time.Minute

func runCommand(command string) error {
	ctx, cancel := context.WithTimeout(context.Background(), CommandTimeout)
	defer cancel()

	if err := exec.CommandContext(ctx, "sh", "-c", command).Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("command timed out after %s: %s", CommandTimeout, command)
		}
		return err
	}

	return nil
}

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
	return runCommand(command)
}

func (c *Cli) Restart() error {
	commands := []string{
		fmt.Sprintf("cp -f %s %s", ScriptBackupPath, ScriptPath),
		fmt.Sprintf("%s restart", ScriptPath),
	}

	command := strings.Join(commands, " && ")
	return runCommand(command)
}

func (c *Cli) Stop() error {
	command := fmt.Sprintf("%s stop", ScriptPath)
	err := runCommand(command)
	if err != nil {
		return err
	}

	return os.Remove(ScriptPath)
}

func (c *Cli) Up() error {
	command := "tailscale up --accept-dns=false"
	return runCommand(command)
}

func (c *Cli) Down() error {
	command := "tailscale down"
	return runCommand(command)
}

func (c *Cli) Status() (*TsStatus, error) {
	command := "tailscale status --json"

	ctx, cancel := context.WithTimeout(context.Background(), CommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)

	output, err := cmd.CombinedOutput()
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
	// Left without a context on purpose: the command carries its own --timeout,
	// and the caller reads the login URL off stderr while it keeps running.
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
	return runCommand(command)
}
