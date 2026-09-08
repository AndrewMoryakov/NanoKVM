package tailscale

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestServiceRunningUsesTailscaledProcIdentity(t *testing.T) {
	original := daemonPresentForService
	t.Cleanup(func() { daemonPresentForService = original })

	called := false
	daemonPresentForService = func(name, executable string) (bool, error) {
		called = true
		if name != "tailscaled" {
			t.Errorf("daemon name = %q, want tailscaled", name)
		}
		if executable != TailscaledPath {
			t.Errorf("executable = %q, want %q", executable, TailscaledPath)
		}
		return true, nil
	}

	running, err := NewCli().ServiceRunning()
	if err != nil || !running {
		t.Fatalf("ServiceRunning() = (%t, %v), want (true, nil)", running, err)
	}
	if !called {
		t.Fatal("ServiceRunning did not use the daemon identity probe")
	}
}

func TestServiceRunningPropagatesProbeError(t *testing.T) {
	original := daemonPresentForService
	t.Cleanup(func() { daemonPresentForService = original })
	want := errors.New("pidof unavailable")
	daemonPresentForService = func(string, string) (bool, error) { return false, want }

	running, err := NewCli().ServiceRunning()
	if running || !errors.Is(err, want) {
		t.Fatalf("ServiceRunning() = (%t, %v), want (false, %v)", running, err, want)
	}
}

func TestProcessInspectionGoneOnlyAcceptsVanishedProcess(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{"not exist", &os.PathError{Op: "readlink", Path: "/proc/1/exe", Err: syscall.ENOENT}, true},
		{"no such process", syscall.ESRCH, true},
		{"permission denied", &os.PathError{Op: "readlink", Path: "/proc/1/exe", Err: syscall.EACCES}, false},
		{"io error", &os.PathError{Op: "read", Path: "/proc/1/cmdline", Err: syscall.EIO}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := processInspectionGone(test.err); got != test.want {
				t.Fatalf("processInspectionGone(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestCanResumeRequiresAllExecutableBinaries(t *testing.T) {
	dir := t.TempDir()
	tailscalePath := filepath.Join(dir, "tailscale")
	tailscaledPath := filepath.Join(dir, "tailscaled")
	scriptPath := filepath.Join(dir, "S98tailscaled")
	for _, path := range []string{tailscalePath, tailscaledPath, scriptPath} {
		if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	if err := canResume(tailscalePath, tailscaledPath, scriptPath); err != nil {
		t.Fatalf("canResume() with usable artifacts: %v", err)
	}
	for _, path := range []string{tailscalePath, tailscaledPath, scriptPath} {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := canResume(tailscalePath, tailscaledPath, scriptPath); err == nil {
			t.Fatalf("canResume() accepted non-executable prerequisite %s", path)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCanResumeAcceptsSymlinkToExecutablePrerequisite(t *testing.T) {
	dir := t.TempDir()
	tailscalePath := filepath.Join(dir, "tailscale")
	tailscaledPath := filepath.Join(dir, "tailscaled")
	scriptPath := filepath.Join(dir, "S98tailscaled")
	realBinaryPath := filepath.Join(dir, "tailscale.real")
	for _, path := range []string{tailscaledPath, scriptPath, realBinaryPath} {
		if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := os.Symlink(realBinaryPath, tailscalePath); err != nil {
		t.Fatal(err)
	}

	if err := canResume(tailscalePath, tailscaledPath, scriptPath); err != nil {
		t.Fatalf("canResume() rejected a symlink to an executable Tailscale binary: %v", err)
	}
}
