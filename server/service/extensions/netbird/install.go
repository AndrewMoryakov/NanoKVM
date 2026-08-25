package netbird

import (
	"NanoKVM-Server/utils"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	NetbirdPath = "/usr/bin/netbird"
	// PinnedVersionPath holds the NetBird version this firmware was built against.
	// The binary itself is not bundled: it is published as a release asset and
	// downloaded on demand, the same way Tailscale is.
	PinnedVersionPath    = "/kvmapp/system/netbird/VERSION"
	InstalledVersionPath = "/etc/kvm/netbird.version"

	// NetBird publishes no riscv64 build, so NanoKVM builds one and attaches it
	// to its own release. Until this lands upstream and one release is cut with
	// the asset, the default URL 404s — DownloadURLFile lets a fork or a test
	// device point somewhere else without rebuilding the server.
	DefaultDownloadURL = "https://github.com/sipeed/NanoKVM/releases/latest/download/netbird_riscv64.tgz"
	DownloadURLFile    = "/etc/kvm/netbird_url"

	// The workspace must stay outside any directory NetBird itself uses for
	// state: it is removed after every install.
	Workspace = "/root/.netbird-install"

	downloadTimeout = 15 * time.Minute
)

func downloadURL() string {
	if custom := readVersion(DownloadURLFile); custom != "" {
		return custom
	}

	return DefaultDownloadURL
}

func isInstalled() bool {
	_, err := os.Stat(NetbirdPath)
	return err == nil
}

// install fetches and installs the client. force is set when the user asked for
// it explicitly; otherwise an already-installed client is left alone. Without
// that distinction a version mismatch between the pin and the published asset
// would make every start re-download the archive.
func install(force bool) error {
	if isInstalled() && (!force || isUpToDate()) {
		return nil
	}

	_ = os.MkdirAll(Workspace, 0o755)
	defer func() {
		_ = os.RemoveAll(Workspace)
	}()

	tarFile := fmt.Sprintf("%s/netbird_riscv64.tgz", Workspace)

	// download
	if err := download(tarFile); err != nil {
		log.Errorf("failed to download netbird: %s", err)
		return err
	}

	// decompress
	dir, err := utils.UnTarGz(tarFile, Workspace)
	if err != nil {
		log.Errorf("failed to decompress netbird: %s", err)
		return err
	}

	// the archive carries the version it was built from
	downloadedVersion := readVersion(filepath.Join(dir, "VERSION"))
	if pinned := getPinnedVersion(); pinned != "" && downloadedVersion != "" && pinned != downloadedVersion {
		log.Warnf("netbird version mismatch: firmware pins %s, release asset provides %s",
			pinned, downloadedVersion)
	}

	// move: rename over a running executable is allowed, unlike writing into it
	binary := filepath.Join(dir, "netbird")
	if err := utils.MoveFile(binary, NetbirdPath); err != nil {
		log.Errorf("failed to move netbird: %s", err)
		return err
	}

	if err := os.Chmod(NetbirdPath, 0o755); err != nil {
		return fmt.Errorf("chmod netbird failed: %w", err)
	}

	// Record what was actually installed; fall back to the pin when the archive
	// carries no VERSION, so the installed version never reads as empty.
	if downloadedVersion == "" {
		downloadedVersion = getPinnedVersion()
	}

	if err := writeInstalledVersion(downloadedVersion); err != nil {
		return err
	}

	log.Debugf("install netbird successfully")
	return nil
}

func uninstall() error {
	_ = os.Remove(NetbirdPath)
	_ = os.Remove(InstalledVersionPath)
	_ = os.Remove(ScriptPath)
	_ = os.Remove(PidFile)
	return nil
}

func download(target string) error {
	// A bare http.Get has no deadline. This runs while the VPN lock is held, so
	// a hung connection would block every other VPN operation until reboot.
	client := &http.Client{Timeout: downloadTimeout}

	resp, err := client.Get(downloadURL())
	if err != nil {
		log.Errorf("failed to download netbird: %s", err)
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	out, err := os.Create(target)
	if err != nil {
		log.Errorf("failed to create file: %s", err)
		return err
	}
	defer func() {
		_ = out.Close()
	}()

	if _, err = io.Copy(out, resp.Body); err != nil {
		log.Errorf("failed to copy response body to file: %s", err)
		return err
	}

	log.Debugf("download netbird successfully")
	return nil
}

func isUpToDate() bool {
	pinned := getPinnedVersion()
	return pinned != "" && pinned == getInstalledVersion()
}

func writeInstalledVersion(version string) error {
	if version == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(InstalledVersionPath), 0o755); err != nil {
		return fmt.Errorf("create version dir failed: %w", err)
	}

	if err := os.WriteFile(InstalledVersionPath, []byte(version), 0o644); err != nil {
		return fmt.Errorf("write version failed: %w", err)
	}

	return nil
}

func getPinnedVersion() string {
	return readVersion(PinnedVersionPath)
}

func getInstalledVersion() string {
	return readVersion(InstalledVersionPath)
}

func readVersion(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(content))
}
