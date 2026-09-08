package tailscale

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// PinnedVersionPath and PinnedSHA256Path are shipped with the firmware. The
	// installer never follows Tailscale's mutable latest URL: an update is an
	// explicit source change with a release version and checksum that are both
	// reviewed with the firmware.
	PinnedVersionPath  = "/kvmapp/system/tailscale/VERSION"
	PinnedSHA256Path   = "/kvmapp/system/tailscale/SHA256"
	ReleaseDownloadURL = "https://pkgs.tailscale.com/stable/tailscale_%s_riscv64.tgz"

	installTimeout                  = 4 * time.Minute
	maxTailscaleArchiveSize   int64 = 128 << 20
	maxTailscaleExtractedSize int64 = 256 << 20
	minTailscaleBinarySize    int64 = 64
	maxTailscaleBinarySize    int64 = 128 << 20
	maxTailscaleSystemdSize   int64 = 64 << 10
)

var (
	installHTTPClient  = &http.Client{Timeout: installTimeout}
	tailscaleVersionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	tailscaleSHA256RE  = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type stagedInstall struct {
	files   [2]string
	targets [2]string
	link    func(string, string) error
}

type tailscaleArchiveEntry struct {
	typeflag byte
	minSize  int64
	maxSize  int64
	stage    int // -1 for non-executable entries
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

// stagePinnedInstall reads the immutable firmware metadata before downloading.
// The version is part of the URL and the archive hash is verified before any
// executable is extracted or can be promoted.
func stagePinnedInstall(ctx context.Context, client *http.Client, targets [2]string) (*stagedInstall, error) {
	version, digest, err := pinnedInstallMetadata(PinnedVersionPath, PinnedSHA256Path)
	if err != nil {
		return nil, err
	}
	return stageInstall(ctx, client, fmt.Sprintf(ReleaseDownloadURL, version), version, digest, targets)
}

func pinnedInstallMetadata(versionPath, digestPath string) (string, string, error) {
	versionContent, err := os.ReadFile(versionPath)
	if err != nil {
		return "", "", fmt.Errorf("read pinned tailscale version: %w", err)
	}
	digestContent, err := os.ReadFile(digestPath)
	if err != nil {
		return "", "", fmt.Errorf("read pinned tailscale SHA-256: %w", err)
	}
	return validatePinnedInstallMetadata(strings.TrimSpace(string(versionContent)), strings.TrimSpace(string(digestContent)))
}

func validatePinnedInstallMetadata(version, digest string) (string, string, error) {
	if !tailscaleVersionRE.MatchString(version) {
		return "", "", fmt.Errorf("invalid pinned tailscale version %q", version)
	}
	if !tailscaleSHA256RE.MatchString(digest) {
		return "", "", fmt.Errorf("invalid pinned tailscale SHA-256 %q", digest)
	}
	return version, digest, nil
}

// Each staged file lives beside its destination so exclusive publication also
// works when /usr/bin and /usr/sbin are on different filesystems.
func stageInstall(ctx context.Context, client *http.Client, url, expectedVersion, expectedDigest string, targets [2]string) (stage *stagedInstall, err error) {
	if _, _, err := validatePinnedInstallMetadata(expectedVersion, expectedDigest); err != nil {
		return nil, err
	}
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

	// Store the compressed stream before extraction. This makes the firmware
	// checksum cover exactly the bytes that the archive parser will consume.
	archive, createErr := os.CreateTemp(filepath.Dir(targets[0]), ".tailscale-archive-")
	if createErr != nil {
		return stage, createErr
	}
	archivePath := archive.Name()
	if closeErr := archive.Close(); closeErr != nil {
		_ = os.Remove(archivePath)
		return stage, closeErr
	}
	defer func() { _ = os.Remove(archivePath) }()

	if err := downloadArchive(ctx, client, url, archivePath); err != nil {
		return stage, err
	}
	if err := verifyArchiveDigest(archivePath, expectedDigest); err != nil {
		return stage, err
	}
	if err := extractArchive(ctx, archivePath, expectedVersion, stage); err != nil {
		return stage, err
	}
	if err := ctx.Err(); err != nil {
		return stage, err
	}
	return stage, nil
}

func downloadArchive(ctx context.Context, client *http.Client, url, archivePath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create tailscale download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download tailscale archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	if resp.ContentLength > maxTailscaleArchiveSize {
		return fmt.Errorf("tailscale archive exceeds %d byte limit", maxTailscaleArchiveSize)
	}

	out, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tailscale archive staging file: %w", err)
	}
	written, copyErr := io.Copy(out, io.LimitReader(resp.Body, maxTailscaleArchiveSize+1))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("write tailscale archive: %w", copyErr)
	}
	if written > maxTailscaleArchiveSize {
		return fmt.Errorf("tailscale archive exceeds %d byte limit", maxTailscaleArchiveSize)
	}
	if syncErr != nil {
		return fmt.Errorf("sync tailscale archive: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close tailscale archive: %w", closeErr)
	}
	return nil
}

func verifyArchiveDigest(archivePath, expected string) error {
	if !tailscaleSHA256RE.MatchString(expected) {
		return fmt.Errorf("invalid expected tailscale SHA-256 %q", expected)
	}
	expectedBytes, err := hex.DecodeString(expected)
	if err != nil {
		return fmt.Errorf("decode expected tailscale SHA-256: %w", err)
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tailscale archive for checksum: %w", err)
	}
	defer archive.Close()

	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(archive, maxTailscaleArchiveSize+1))
	if err != nil {
		return fmt.Errorf("hash tailscale archive: %w", err)
	}
	if written > maxTailscaleArchiveSize {
		return fmt.Errorf("tailscale archive exceeds %d byte limit", maxTailscaleArchiveSize)
	}
	if subtle.ConstantTimeCompare(hash.Sum(nil), expectedBytes) != 1 {
		return errors.New("tailscale archive SHA-256 does not match firmware pin")
	}
	return nil
}

func expectedArchiveEntries(version string) map[string]tailscaleArchiveEntry {
	root := "tailscale_" + version + "_riscv64"
	return map[string]tailscaleArchiveEntry{
		root:                                            {typeflag: tar.TypeDir, stage: -1},
		root + "/tailscale":                             {typeflag: tar.TypeReg, minSize: minTailscaleBinarySize, maxSize: maxTailscaleBinarySize, stage: 0},
		root + "/tailscaled":                            {typeflag: tar.TypeReg, minSize: minTailscaleBinarySize, maxSize: maxTailscaleBinarySize, stage: 1},
		root + "/systemd":                               {typeflag: tar.TypeDir, stage: -1},
		root + "/systemd/tailscaled.service":            {typeflag: tar.TypeReg, maxSize: maxTailscaleSystemdSize, stage: -1},
		root + "/systemd/tailscaled.defaults":           {typeflag: tar.TypeReg, maxSize: maxTailscaleSystemdSize, stage: -1},
		root + "/systemd/tailscale-online.target":       {typeflag: tar.TypeReg, maxSize: maxTailscaleSystemdSize, stage: -1},
		root + "/systemd/tailscale-wait-online.service": {typeflag: tar.TypeReg, maxSize: maxTailscaleSystemdSize, stage: -1},
	}
}

// extractArchive accepts exactly the regular files and directories in the
// official versioned riscv64 tarball. The checksum is the authenticity bound;
// the strict manifest prevents an otherwise valid but malformed archive from
// writing links, special files, or an executable for another architecture.
func extractArchive(ctx context.Context, archivePath, expectedVersion string, stage *stagedInstall) error {
	if !tailscaleVersionRE.MatchString(expectedVersion) {
		return fmt.Errorf("invalid expected tailscale version %q", expectedVersion)
	}
	if stage == nil {
		return errors.New("nil tailscale install stage")
	}
	info, err := os.Lstat(archivePath)
	if err != nil {
		return fmt.Errorf("stat tailscale archive: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("tailscale archive is not a regular file")
	}
	if info.Size() > maxTailscaleArchiveSize {
		return fmt.Errorf("tailscale archive exceeds %d byte limit", maxTailscaleArchiveSize)
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tailscale archive: %w", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open tailscale gzip stream: %w", err)
	}
	defer gz.Close()

	expected := expectedArchiveEntries(expectedVersion)
	root := "tailscale_" + expectedVersion + "_riscv64"
	seen := make(map[string]bool, len(expected))
	limited := &io.LimitedReader{R: gz, N: maxTailscaleExtractedSize + 1}
	reader := tar.NewReader(limited)
	var extracted int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, readErr := reader.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read tailscale archive: %w", readErr)
		}

		name := header.Name
		// GNU tar can spell either directory in the official archive with a
		// trailing slash. Treat only those exact alternate spellings as
		// canonical; every other non-canonical path remains invalid.
		if header.Typeflag == tar.TypeDir {
			switch name {
			case root + "/":
				name = root
			case root + "/systemd/":
				name = root + "/systemd"
			}
		}
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.Contains(name, "\\") || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe tailscale archive entry %q", header.Name)
		}
		entry, ok := expected[name]
		if !ok {
			return fmt.Errorf("unexpected tailscale archive entry %q", header.Name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate tailscale archive entry %q", header.Name)
		}
		if !validTailscaleArchiveType(header.Typeflag, entry.typeflag) {
			return fmt.Errorf("tailscale archive entry %q has unsafe type %q", header.Name, header.Typeflag)
		}
		if header.Size < entry.minSize || header.Size > entry.maxSize || extracted+header.Size > maxTailscaleExtractedSize {
			return fmt.Errorf("tailscale archive entry %q exceeds size limit", header.Name)
		}
		extracted += header.Size
		seen[name] = true

		if entry.typeflag == tar.TypeDir {
			if header.Size != 0 {
				return fmt.Errorf("tailscale archive directory %q has content", header.Name)
			}
			continue
		}
		if entry.stage >= 0 {
			if entry.stage >= len(stage.files) || stage.files[entry.stage] == "" {
				return errors.New("tailscale install stage is incomplete")
			}
			if err := writeStagedExecutable(stage.files[entry.stage], reader); err != nil {
				return fmt.Errorf("extract tailscale executable %q: %w", header.Name, err)
			}
			continue
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return fmt.Errorf("read tailscale archive entry %q: %w", header.Name, err)
		}
	}
	if err := drainZeroTarPadding(limited); err != nil {
		return fmt.Errorf("validate tailscale archive stream: %w", err)
	}
	if limited.N == 0 {
		return fmt.Errorf("tailscale archive exceeds %d byte uncompressed limit", maxTailscaleExtractedSize)
	}
	if len(seen) != len(expected) {
		return errors.New("tailscale archive is incomplete")
	}
	for _, name := range stage.files {
		if err := validateRISCVELF(name); err != nil {
			return err
		}
	}
	return nil
}

func validTailscaleArchiveType(actual, expected byte) bool {
	if expected == tar.TypeDir {
		return actual == tar.TypeDir
	}
	return actual == tar.TypeReg || actual == tar.TypeRegA
}

func drainZeroTarPadding(reader io.Reader) error {
	var buffer [32 * 1024]byte
	for {
		count, err := reader.Read(buffer[:])
		for _, value := range buffer[:count] {
			if value != 0 {
				return errors.New("unexpected data after tailscale tar end marker")
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func validateRISCVELF(binaryPath string) error {
	file, err := os.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("open tailscale executable %s: %w", binaryPath, err)
	}
	defer file.Close()

	var header [64]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return fmt.Errorf("read tailscale executable %s ELF header: %w", binaryPath, err)
	}
	if string(header[:4]) != "\x7fELF" ||
		header[4] != 2 || header[5] != 1 || header[6] != 1 ||
		header[16] != 2 || header[17] != 0 ||
		header[18] != 243 || header[19] != 0 ||
		header[20] != 1 || header[21] != 0 || header[22] != 0 || header[23] != 0 ||
		header[52] != 64 || header[53] != 0 {
		return fmt.Errorf("tailscale executable %s is not a 64-bit little-endian riscv64 ELF", binaryPath)
	}
	return nil
}

func writeStagedExecutable(name string, source io.Reader) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, source)
	chmodErr := file.Chmod(0o755)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if chmodErr != nil {
		return chmodErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
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
