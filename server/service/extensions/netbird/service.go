package netbird

import (
	"NanoKVM-Server/proto"
	"NanoKVM-Server/service/extensions/vpnpref"
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type Service struct{}

// installMu is intentionally separate from vpnpref's lifecycle lock. Download
// and archive validation can take minutes on NanoKVM's uplink; serialising
// those slow bytes must not make Stop/Restart/other-VPN recovery unavailable.
// It stays held until promotion so two concurrent initial installs cannot race
// to publish different binaries.
var installMu sync.Mutex

var stagedInstallState struct {
	sync.Mutex
	generation uint64
	cancel     context.CancelFunc
}

func NewService() *Service {
	return &Service{}
}

// lockVPN serializes operations that start or stop a client.
//
// It deliberately does NOT check the autostart preference. That preference says
// what runs at boot, not who may run now: gating these handlers on it made the
// first NetBird login unreachable (login needs the preference, the preference
// needs a connected client) and, worse, let a switch cut the tunnel the caller
// was connected through. Mutual exclusion is enforced where it belongs — in
// SetPreference, which stops the other client only once this one is connected.
//
// Both daemons may therefore be up briefly while a user sets the new one up. For
// a client that was never bound this costs memory only; one that was bound before
// may reconnect, so two tunnels are possible during a switch. That overlap is
// deliberate: the alternative — refusing — is what locked devices out.
func lockVPN(c *gin.Context, rsp *proto.Response) bool {
	if !vpnpref.TryLock() {
		rsp.ErrRsp(c, -5, "another VPN operation is in progress, please retry")
		return false
	}

	return true
}

// stageIfNeeded obtains a validated binary outside the VPN lifecycle lock.
// The caller must invoke release after it has either promoted or discarded the
// result. A nil stage means a usable binary was already installed.
func stageIfNeeded() (stage *StagedInstall, generation uint64, release func(), err error) {
	if isInstalled() {
		return nil, 0, func() {}, nil
	}
	if !installMu.TryLock() {
		return nil, 0, nil, fmt.Errorf("a netbird installation is already in progress")
	}

	// The winner may have finished between the optimistic check and TryLock.
	if isInstalled() {
		return nil, 0, installMu.Unlock, nil
	}

	installContext, generation, finish := beginStagedInstall()
	release = func() {
		finish()
		installMu.Unlock()
	}
	stage, err = StageInstallContext(installContext)
	if err != nil {
		release()
		return nil, 0, nil, err
	}
	return stage, generation, release, nil
}

// beginStagedInstall creates a cancellable intent before any slow I/O starts.
// A later NetBird, Tailscale, or preference lifecycle action invalidates that
// intent while holding the VPN lock. Promotion verifies the generation again
// under the same lock, so an old request can never resurrect NetBird after a
// newer lifecycle action.
func beginStagedInstall() (context.Context, uint64, func()) {
	stagedInstallState.Lock()
	installContext, cancel := context.WithCancel(context.Background())
	generation := stagedInstallState.generation
	stagedInstallState.cancel = cancel
	stagedInstallState.Unlock()

	return installContext, generation, func() {
		cancel()
		stagedInstallState.Lock()
		if stagedInstallState.generation == generation {
			stagedInstallState.cancel = nil
		}
		stagedInstallState.Unlock()
	}
}

func stagedInstallCurrent(generation uint64) bool {
	stagedInstallState.Lock()
	defer stagedInstallState.Unlock()
	return stagedInstallState.generation == generation && stagedInstallState.cancel != nil
}

// InvalidateStagedInstall cancels a pending NetBird download and prevents it
// from being promoted. It must run after vpnpref.TryLock succeeds. That makes
// its ordering match real daemon transitions: an operation rejected as busy
// does not cancel a request that already owns the lifecycle lock.
//
// Tailscale and the VPN-preference service call this after they acquire the
// same lifecycle lock. Without that shared ordering, a slow NetBird download
// could finish after a newer Tailscale operation and start NetBird again.
func InvalidateStagedInstall() {
	stagedInstallState.Lock()
	stagedInstallState.generation++
	if stagedInstallState.cancel != nil {
		stagedInstallState.cancel()
		stagedInstallState.cancel = nil
	}
	stagedInstallState.Unlock()
}

func promoteIfNeeded(stage *StagedInstall) error {
	if stage == nil {
		return nil
	}
	defer func() { _ = stage.Cleanup() }()
	if err := stage.Promote(); err != nil {
		// A manual install may have won while the asset downloaded. It is safe to
		// use that existing binary; never replace it from this request.
		if err == ErrNetbirdAlreadyInstalled && isInstalled() {
			return nil
		}
		return err
	}
	return nil
}

func (s *Service) Install(c *gin.Context) {
	var rsp proto.Response

	stage, generation, release, err := stageIfNeeded()
	if err != nil {
		rsp.ErrRsp(c, -1, fmt.Sprintf("install failed: %v", err))
		return
	}
	defer release()
	if stage != nil {
		defer func() { _ = stage.Cleanup() }()
	}

	if !lockVPN(c, &rsp) {
		return
	}
	defer vpnpref.Unlock()

	if stage != nil && !stagedInstallCurrent(generation) {
		rsp.ErrRsp(c, -2, "install was canceled by a newer VPN operation")
		return
	}
	if stage == nil && isInstalled() && !isUpToDate() {
		rsp.ErrRsp(c, -3, "netbird update required; uninstall and install again to replace it safely")
		return
	}
	if err := promoteIfNeeded(stage); err != nil {
		rsp.ErrRsp(c, -1, fmt.Sprintf("install failed: %v", err))
		log.Errorf("failed to install netbird: %s", err)
		return
	}

	// Start the daemon regardless of which client owns autostart, so signing in
	// is reachable while the other client still carries the session.
	//
	// S99netbird runs `netbird service run`; the tunnel is raised by `netbird up`,
	// which is the login step. On a device that has never been bound — the case
	// this exists for — the daemon therefore carries no tunnel. A device that was
	// bound earlier may well reconnect on daemon start; that is not verified here,
	// and it is accepted: the overlap is user-initiated and ends when the switch
	// is completed.
	if err := NewCli().Start(); err != nil {
		rsp.ErrRsp(c, -2, fmt.Sprintf("start failed: %v", err))
		log.Errorf("failed to start netbird after install: %s", err)
		return
	}

	rsp.OkRsp(c)
}

func (s *Service) Uninstall(c *gin.Context) {
	var rsp proto.Response

	if !vpnpref.TryLock() {
		rsp.ErrRsp(c, -5, "another VPN operation is in progress, please retry")
		return
	}
	defer vpnpref.Unlock()
	InvalidateStagedInstall()

	// Order matters. Stop first and check the result: removing the init script
	// from under a live daemon would leave a process nothing on the device can
	// stop any more.
	if err := NewCli().Stop(); err != nil {
		rsp.ErrRsp(c, -2, fmt.Sprintf("stop failed, nothing was removed: %v", err))
		log.Errorf("failed to stop netbird before uninstall: %s", err)
		return
	}

	if err := uninstall(); err != nil {
		rsp.ErrRsp(c, -1, fmt.Sprintf("uninstall failed: %v", err))
		log.Errorf("failed to uninstall netbird: %s", err)
		return
	}

	// A device can intentionally be managed over LAN only. Never select or
	// start Tailscale as a side effect of uninstalling NetBird; preserve the
	// recorded boot preference so the user remains in control of recovery.
	rsp.OkRsp(c)
}

func (s *Service) Start(c *gin.Context) {
	var rsp proto.Response

	stage, generation, release, err := stageIfNeeded()
	if err != nil {
		rsp.ErrRsp(c, -1, fmt.Sprintf("start failed: %v", err))
		return
	}
	defer release()
	if stage != nil {
		defer func() { _ = stage.Cleanup() }()
	}

	if !lockVPN(c, &rsp) {
		return
	}
	defer vpnpref.Unlock()

	if stage != nil && !stagedInstallCurrent(generation) {
		rsp.ErrRsp(c, -2, "start was canceled by a newer VPN operation")
		return
	}
	if stage == nil && isInstalled() && !isUpToDate() {
		rsp.ErrRsp(c, -3, "netbird update required; uninstall and install again to replace it safely")
		return
	}
	if err := promoteIfNeeded(stage); err != nil {
		rsp.ErrRsp(c, -1, fmt.Sprintf("start failed: %v", err))
		log.Errorf("failed to install netbird before start: %s", err)
		return
	}

	if err := NewCli().Start(); err != nil {
		rsp.ErrRsp(c, -1, fmt.Sprintf("start failed: %v", err))
		log.Errorf("failed to start netbird: %s", err)
		return
	}

	rsp.OkRsp(c)
}

func (s *Service) Restart(c *gin.Context) {
	var rsp proto.Response

	if !lockVPN(c, &rsp) {
		return
	}
	defer vpnpref.Unlock()
	InvalidateStagedInstall()

	if err := NewCli().Restart(); err != nil {
		rsp.ErrRsp(c, -1, fmt.Sprintf("restart failed: %v", err))
		log.Errorf("failed to restart netbird: %s", err)
		return
	}

	rsp.OkRsp(c)
}

func (s *Service) Stop(c *gin.Context) {
	var rsp proto.Response

	if !vpnpref.TryLock() {
		rsp.ErrRsp(c, -5, "another VPN operation is in progress, please retry")
		return
	}
	defer vpnpref.Unlock()
	InvalidateStagedInstall()

	if err := NewCli().Stop(); err != nil {
		rsp.ErrRsp(c, -1, fmt.Sprintf("stop failed: %v", err))
		log.Errorf("failed to stop netbird: %s", err)
		return
	}

	rsp.OkRsp(c)
}

func (s *Service) Login(c *gin.Context) {
	var rsp proto.Response

	// `netbird up` brings the tunnel up, so this belongs behind the same guard as
	// Start: on a device set to Tailscale it would raise a second one.
	if !lockVPN(c, &rsp) {
		return
	}
	defer vpnpref.Unlock()

	cli := NewCli()

	url, err := cli.Login()
	if err != nil {
		log.Errorf("failed to run netbird login: %s", err)
		rsp.ErrRsp(c, -2, fmt.Sprintf("login failed: %v", err))
		return
	}

	rsp.OkRspWithData(c, &proto.LoginNetbirdRsp{
		Url: url,
	})

	// The URL is a one-time device-binding secret; log that we got one, not what it is.
	log.Debugf("netbird login url issued: %t", url != "")
}

func (s *Service) Down(c *gin.Context) {
	var rsp proto.Response

	// Down and the other lifecycle endpoints share this lock. It also cancels an
	// outstanding interactive Login before changing the tunnel state.
	if !vpnpref.TryLock() {
		rsp.ErrRsp(c, -5, "another VPN operation is in progress, please retry")
		return
	}
	defer vpnpref.Unlock()
	InvalidateStagedInstall()

	if err := NewCli().Down(); err != nil {
		rsp.ErrRsp(c, -1, fmt.Sprintf("netbird down failed: %v", err))
		log.Errorf("failed to run netbird down: %s", err)
		return
	}

	rsp.OkRsp(c)
}

func (s *Service) GetStatus(c *gin.Context) {
	var rsp proto.Response

	if !isInstalled() {
		rsp.OkRspWithData(c, &proto.GetNetbirdStatusRsp{
			State:   proto.NetbirdNotInstall,
			Version: getPinnedVersion(),
		})
		return
	}

	cli := NewCli()
	running, err := cli.ServiceRunning()
	if err != nil {
		rsp.ErrRsp(c, -1, fmt.Sprintf("service status failed: %v", err))
		return
	}

	if !running {
		rsp.OkRspWithData(c, &proto.GetNetbirdStatusRsp{
			State:   proto.NetbirdNotRunning,
			Version: getInstalledVersion(),
		})
		return
	}

	status, err := cli.Status()
	if err != nil {
		// Do not turn a timeout, malformed JSON or daemon I/O error into a
		// confident disconnected/login state. The UI treats this transport
		// error as unknown/stale and leaves recovery actions available.
		rsp.ErrRsp(c, -2, fmt.Sprintf("netbird status unavailable: %v", err))
		return
	}

	state := proto.NetbirdNotLogin
	if status.Management.Connected && status.Signal.Connected {
		state = proto.NetbirdRunning
	} else if status.Management.URL != "" {
		state = proto.NetbirdStopped
	}

	rsp.OkRspWithData(c, &proto.GetNetbirdStatusRsp{
		State:   state,
		Name:    status.FQDN,
		IP:      getIPv4(status.IP),
		Version: firstNonEmpty(status.DaemonVersion, getInstalledVersion()),
	})
}

func getIPv4(ip string) string {
	if ip == "" {
		return ""
	}

	if addr, _, err := net.ParseCIDR(ip); err == nil && addr != nil {
		if ipv4 := addr.To4(); ipv4 != nil {
			return ipv4.String()
		}
		return addr.String()
	}

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}

	if parsed.To4() != nil {
		return parsed.String()
	}

	return ""
}

func firstNonEmpty(items ...string) string {
	for _, item := range items {
		if item != "" {
			return item
		}
	}

	return ""
}
