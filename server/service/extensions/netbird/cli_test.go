package netbird

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestParseStatusAcceptsOnlyJSONObservation(t *testing.T) {
	status, err := parseStatus("warning\n{\"fqdn\":\"nano\",\"management\":{\"connected\":true},\"signal\":{\"connected\":true}}")
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	if status.FQDN != "nano" || !status.Management.Connected || !status.Signal.Connected {
		t.Fatalf("unexpected parsed status: %#v", status)
	}

	if _, err := parseStatus("netbird daemon timed out"); err == nil {
		t.Fatal("parseStatus accepted a non-JSON diagnostic")
	}
}

func TestIsNetbirdDaemonCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmdline []byte
		want    bool
	}{
		{"daemon", []byte("/usr/bin/netbird\x00service\x00run\x00"), true},
		{"status client", []byte("/usr/bin/netbird\x00status\x00--json\x00"), false},
		{"login client", []byte("/usr/bin/netbird\x00up\x00--no-browser\x00"), false},
		{"missing arguments", []byte("/usr/bin/netbird\x00"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isNetbirdDaemonCommand(test.cmdline); got != test.want {
				t.Fatalf("isNetbirdDaemonCommand() = %t, want %t", got, test.want)
			}
		})
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

func TestCanResumeRequiresExecutableBinary(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "netbird")
	scriptPath := filepath.Join(dir, "S99netbird")
	writeExecutable(t, binaryPath)
	writeExecutable(t, scriptPath)

	if err := canResume(binaryPath, scriptPath, "1.2.3", "1.2.3"); err != nil {
		t.Fatalf("canResume() with usable artifacts: %v", err)
	}
	if err := os.Chmod(binaryPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := canResume(binaryPath, scriptPath, "1.2.3", "1.2.3"); err == nil {
		t.Fatal("canResume() accepted a non-executable NetBird binary")
	}
}

func TestCanRestartRejectsMissingPrerequisiteBeforeRestart(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "netbird")
	backupPath := filepath.Join(dir, "S99netbird")
	writeExecutable(t, binaryPath)
	writeExecutable(t, backupPath)

	if err := canRestart(binaryPath, backupPath, "1.2.3", "1.2.3"); err != nil {
		t.Fatalf("canRestart() with usable artifacts: %v", err)
	}
	if err := os.Remove(binaryPath); err != nil {
		t.Fatal(err)
	}
	if err := canRestart(binaryPath, backupPath, "1.2.3", "1.2.3"); err == nil {
		t.Fatal("canRestart() accepted a missing binary")
	}
}

func TestCanResumeAndRestartAcceptSymlinkToExecutableBinary(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "netbird")
	realBinaryPath := filepath.Join(dir, "netbird.real")
	scriptPath := filepath.Join(dir, "S99netbird")
	writeExecutable(t, realBinaryPath)
	writeExecutable(t, scriptPath)
	if err := os.Symlink(realBinaryPath, binaryPath); err != nil {
		t.Fatal(err)
	}

	if err := canResume(binaryPath, scriptPath, "1.2.3", "1.2.3"); err != nil {
		t.Fatalf("canResume() rejected a symlink to an executable NetBird binary: %v", err)
	}
	if err := canRestart(binaryPath, scriptPath, "1.2.3", "1.2.3"); err != nil {
		t.Fatalf("canRestart() rejected a symlink to an executable NetBird binary: %v", err)
	}
}
