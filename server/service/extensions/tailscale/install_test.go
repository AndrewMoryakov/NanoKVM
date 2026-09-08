package tailscale

import (
	"NanoKVM-Server/service/extensions/vpnpref"
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testArchive(t *testing.T, entries []string) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tarball := tar.NewWriter(gz)
	root := "tailscale_1.90.0_riscv64/"
	if err := tarball.WriteHeader(&tar.Header{Name: root, Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		data := []byte("executable-" + name)
		if err := tarball.WriteHeader(&tar.Header{Name: root + name, Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarball.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testTargets(t *testing.T) [2]string {
	t.Helper()
	return [2]string{filepath.Join(t.TempDir(), "tailscale"), filepath.Join(t.TempDir(), "tailscaled")}
}

func serveArchive(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	t.Cleanup(server.Close)
	return server
}

func TestStageInstallAndPublishPair(t *testing.T) {
	server := serveArchive(t, testArchive(t, []string{"tailscale", "tailscaled", "systemd/tailscaled.service"}))
	targets := testTargets(t)
	stage, err := stageInstall(context.Background(), server.Client(), server.URL, targets)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.cleanup()
	for _, target := range targets {
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatal("staging published a destination")
		}
	}
	if err := stage.promote(); err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		info, err := os.Stat(target)
		if err != nil || info.Mode()&0o111 == 0 {
			t.Fatalf("published executable %s: %v", target, err)
		}
	}
}

func TestStageRejectsBrokenArchivesAndCleansUp(t *testing.T) {
	for _, entries := range [][]string{{"tailscale"}, {"tailscale", "tailscaled", "tailscale"}, {"../escape", "tailscale", "tailscaled"}} {
		t.Run(entries[0], func(t *testing.T) {
			server := serveArchive(t, testArchive(t, entries))
			targets := testTargets(t)
			if _, err := stageInstall(context.Background(), server.Client(), server.URL, targets); err == nil {
				t.Fatal("invalid archive accepted")
			}
			for _, target := range targets {
				files, err := os.ReadDir(filepath.Dir(target))
				if err != nil || len(files) != 0 {
					t.Fatalf("failed stage left files: %v, %v", files, err)
				}
			}
		})
	}
}

func TestPublishCollisionRollsBackOnlyOwnFiles(t *testing.T) {
	for _, collision := range []int{0, 1} {
		t.Run(string(rune('0'+collision)), func(t *testing.T) {
			server := serveArchive(t, testArchive(t, []string{"tailscale", "tailscaled"}))
			targets := testTargets(t)
			stage, err := stageInstall(context.Background(), server.Client(), server.URL, targets)
			if err != nil {
				t.Fatal(err)
			}
			defer stage.cleanup()
			stage.link = func(source, target string) error {
				if target == targets[collision] {
					if err := os.WriteFile(target, []byte("existing"), 0o755); err != nil {
						return err
					}
				}
				return os.Link(source, target)
			}
			if err := stage.promote(); err == nil {
				t.Fatal("collision accepted")
			}
			data, err := os.ReadFile(targets[collision])
			if err != nil || string(data) != "existing" {
				t.Fatalf("collision target changed: %s, %v", data, err)
			}
			if _, err := os.Lstat(targets[1-collision]); !os.IsNotExist(err) {
				t.Fatal("partial pair remained after failed promotion")
			}
		})
	}
}

func TestPendingDownloadLeavesLifecycleAvailableAndCancels(t *testing.T) {
	for _, flushHeaders := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "body"}[flushHeaders], func(t *testing.T) {
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if flushHeaders {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				close(entered)
				<-r.Context().Done()
			}))
			defer server.Close()
			if !vpnpref.TryLock() {
				t.Fatal("lifecycle lock busy")
			}
			ctx, token, finish := vpnpref.BeginStagedInstall(context.Background())
			vpnpref.Unlock()
			defer finish()
			targets := testTargets(t)
			result := make(chan error, 1)
			go func() { _, err := stageInstall(ctx, server.Client(), server.URL, targets); result <- err }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("download did not start")
			}
			if !vpnpref.TryLock() {
				t.Fatal("download blocks lifecycle")
			}
			vpnpref.InvalidateStagedInstalls()
			current := vpnpref.StagedInstallCurrent(token)
			vpnpref.Unlock()
			if current {
				t.Fatal("canceled stage remains publishable")
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("canceled download succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("download did not cancel")
			}
			for _, target := range targets {
				files, _ := os.ReadDir(filepath.Dir(target))
				if len(files) != 0 {
					t.Fatal("cancellation leaked staging files")
				}
			}
		})
	}
}

func TestRollbackDoesNotRemoveAReplacedDestination(t *testing.T) {
	server := serveArchive(t, testArchive(t, []string{"tailscale", "tailscaled"}))
	targets := testTargets(t)
	stage, err := stageInstall(context.Background(), server.Client(), server.URL, targets)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.cleanup()
	stage.link = func(source, target string) error {
		if target == targets[1] {
			if err := os.Remove(targets[0]); err != nil {
				return err
			}
			if err := os.WriteFile(targets[0], []byte("replacement"), 0o755); err != nil {
				return err
			}
			return errors.New("second publish failed")
		}
		return os.Link(source, target)
	}
	err = stage.promote()
	if err == nil || !strings.Contains(err.Error(), "cannot safely roll back") {
		t.Fatalf("rollback uncertainty not reported: %v", err)
	}
	data, err := os.ReadFile(targets[0])
	if err != nil || string(data) != "replacement" {
		t.Fatalf("replacement was removed: %s, %v", data, err)
	}
}

func TestDownloadHasFiniteClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	client := &http.Client{Timeout: 25 * time.Millisecond}
	if _, err := stageInstall(context.Background(), client, server.URL, testTargets(t)); err == nil {
		t.Fatal("stalled request succeeded")
	}
}

func TestCorruptGzipTrailerIsRejected(t *testing.T) {
	data := testArchive(t, []string{"tailscale", "tailscaled"})
	data[len(data)-8] ^= 0xff
	server := serveArchive(t, data)
	if _, err := stageInstall(context.Background(), server.Client(), server.URL, testTargets(t)); err == nil || err == io.EOF {
		t.Fatalf("corrupt archive accepted: %v", err)
	}
}
