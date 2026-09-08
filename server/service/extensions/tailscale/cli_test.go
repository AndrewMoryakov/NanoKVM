package tailscale

import (
	"errors"
	"os"
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
