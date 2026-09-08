package tailscale

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	OriginalURL            = "https://pkgs.tailscale.com/stable/tailscale_latest_riscv64.tgz"
	installTimeout         = 4 * time.Minute
	maxArchiveSize   int64 = 128 << 20
	maxExtractedSize int64 = 256 << 20
)

var installHTTPClient = &http.Client{Timeout: installTimeout}

type stagedInstall struct {
	files   [2]string
	targets [2]string
	link    func(string, string) error
}

func isInstalled() bool {
	return installedPair([2]string{TailscalePath, TailscaledPath})
}

// installedPair deliberately checks for a complete pair, not executable bits:
// Cli.Start restores execute permissions for a manually recovered install.
func installedPair(targets [2]string) bool {
	for _, name := range targets {
		info, err := os.Stat(name)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// hasInstalledArtifacts distinguishes an incomplete/crashed installation from
// a device with no client at all. Such a state must expose Uninstall in the UI:
// retrying publication intentionally refuses to overwrite either path.
func hasInstalledArtifacts() bool {
	return installArtifactsExist([2]string{TailscalePath, TailscaledPath})
}

func installArtifactsExist(targets [2]string) bool {
	for _, name := range targets {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

// Each staged file lives beside its destination so exclusive publication also
// works when /usr/bin and /usr/sbin are on different filesystems.
func stageInstall(ctx context.Context, client *http.Client, url string, targets [2]string) (stage *stagedInstall, err error) {
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	stage = &stagedInstall{targets: targets, link: os.Link}
	defer func() {
		if err != nil {
			stage.cleanup()
		}
	}()
	for i, target := range targets {
		if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
			return stage, fmt.Errorf("destination already exists or cannot be inspected: %s", target)
		}
		file, createErr := os.CreateTemp(filepath.Dir(target), ".tailscale-install-")
		if createErr != nil {
			return stage, createErr
		}
		stage.files[i] = file.Name()
		if closeErr := file.Close(); closeErr != nil {
			return stage, closeErr
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return stage, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return stage, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return stage, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	compressed := &io.LimitedReader{R: resp.Body, N: maxArchiveSize + 1}
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return stage, err
	}
	defer gz.Close()
	expanded := &io.LimitedReader{R: gz, N: maxExtractedSize + 1}
	archive := tar.NewReader(expanded)
	seen := [2]bool{}
	root := ""
	for {
		if err := ctx.Err(); err != nil {
			return stage, err
		}
		header, readErr := archive.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return stage, readErr
		}
		name := strings.TrimSuffix(header.Name, "/")
		if path.IsAbs(name) || path.Clean(name) != name || strings.Contains(name, "\\") || name == ".." || strings.HasPrefix(name, "../") {
			return stage, fmt.Errorf("unsafe tailscale archive entry %q", header.Name)
		}
		parts := strings.Split(name, "/")
		if root == "" {
			root = parts[0]
		}
		if parts[0] != root || !strings.HasPrefix(root, "tailscale_") || !strings.HasSuffix(root, "_riscv64") {
			return stage, fmt.Errorf("unexpected tailscale archive root %q", parts[0])
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return stage, fmt.Errorf("unsupported tailscale archive entry %q", header.Name)
		}
		index := -1
		if name == root+"/tailscale" {
			index = 0
		}
		if name == root+"/tailscaled" {
			index = 1
		}
		if index < 0 {
			continue
		}
		if seen[index] || header.Size <= 0 || header.Size > maxArchiveSize {
			return stage, fmt.Errorf("invalid tailscale executable %q", name)
		}
		seen[index] = true
		if err := writeStagedExecutable(stage.files[index], archive); err != nil {
			return stage, err
		}
	}
	// Consume trailers to verify gzip checksum and enforce both byte budgets.
	if _, err := io.Copy(io.Discard, expanded); err != nil {
		return stage, err
	}
	if expanded.N <= 0 || compressed.N <= 0 {
		return stage, fmt.Errorf("tailscale archive exceeds size limit")
	}
	if !seen[0] || !seen[1] {
		return stage, fmt.Errorf("tailscale archive is missing an executable")
	}
	if err := ctx.Err(); err != nil {
		return stage, err
	}
	return stage, nil
}

func writeStagedExecutable(name string, source io.Reader) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(file, source); err != nil {
		return err
	}
	if err := file.Chmod(0o755); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func (stage *stagedInstall) cleanup() {
	for _, name := range stage.files {
		if name != "" {
			_ = os.Remove(name)
		}
	}
}

// promote runs under the VPN lifecycle lock, after checking intent and daemon
// liveness. Exclusive links never overwrite an existing or racing executable.
// A failed pair publication rolls back only links belonging to this stage.
// A power loss between links can leave a partial install, which is rejected on
// retry and must be uninstalled explicitly; it is never silently overwritten.
func (stage *stagedInstall) promote() (err error) {
	published := 0
	defer func() {
		if err == nil {
			return
		}
		for i := published - 1; i >= 0; i-- {
			stagedInfo, statErr := os.Stat(stage.files[i])
			targetInfo, targetErr := os.Lstat(stage.targets[i])
			if os.IsNotExist(targetErr) {
				continue
			}
			if statErr != nil || targetErr != nil || !os.SameFile(stagedInfo, targetInfo) {
				err = errors.Join(err, fmt.Errorf("cannot safely roll back %s", stage.targets[i]))
				continue
			}
			if removeErr := os.Remove(stage.targets[i]); removeErr != nil {
				err = errors.Join(err, removeErr)
			}
			if syncErr := syncInstallDirectory(filepath.Dir(stage.targets[i])); syncErr != nil {
				err = errors.Join(err, syncErr)
			}
		}
	}()
	for i, target := range stage.targets {
		if err = stage.link(stage.files[i], target); err != nil {
			return fmt.Errorf("publish %s: %w", target, err)
		}
		published++
	}
	for _, target := range stage.targets {
		if err = syncInstallDirectory(filepath.Dir(target)); err != nil {
			return err
		}
	}
	return nil
}

func syncInstallDirectory(name string) error {
	dir, err := os.Open(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
