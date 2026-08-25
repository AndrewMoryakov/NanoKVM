package netbird

import (
	"NanoKVM-Server/proto"
	"NanoKVM-Server/service/extensions/tailscale"
	"NanoKVM-Server/service/extensions/vpnpref"
	"fmt"
	"net"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type Service struct{}

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

func (s *Service) Install(c *gin.Context) {
	var rsp proto.Response

	if !lockVPN(c, &rsp) {
		return
	}
	defer vpnpref.Unlock()

	if err := install(true); err != nil {
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

	// Only now hand autostart back, and bring Tailscale up so the device is not
	// left without any VPN until the next reboot. The preference is written last,
	// exactly as in SetPreference, so the file never runs ahead of reality.
	if vpnpref.Read() == vpnpref.Netbird {
		if err := tailscale.NewCli().Start(); err != nil {
			log.Warnf("failed to start tailscale after netbird uninstall: %s", err)
		}

		if err := vpnpref.Write(vpnpref.Tailscale); err != nil {
			log.Errorf("failed to reset VPN preference on uninstall: %s", err)
			rsp.ErrRsp(c, -3, fmt.Sprintf("uninstalled, but preference not reset: %v", err))
			return
		}
	}

	rsp.OkRsp(c)
}

func (s *Service) Start(c *gin.Context) {
	var rsp proto.Response

	if !lockVPN(c, &rsp) {
		return
	}
	defer vpnpref.Unlock()

	if !isInstalled() {
		if err := install(false); err != nil {
			rsp.ErrRsp(c, -1, fmt.Sprintf("start failed: %v", err))
			log.Errorf("failed to install netbird before start: %s", err)
			return
		}
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

	// Locked for a non-obvious reason: Cli.Down() runs with restartOnTimeout, so a
	// timed-out `netbird down` restarts the service — this handler can start the
	// daemon, not only stop it.
	if !vpnpref.TryLock() {
		rsp.ErrRsp(c, -5, "another VPN operation is in progress, please retry")
		return
	}
	defer vpnpref.Unlock()

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
		// This branch only runs when `netbird status --json` produced no parsable
		// output at all, so there is no status field to read — only the CLI's own
		// wording. Keep the two phrases that unambiguously mean "not bound yet";
		// "login" alone also appears in unrelated messages.
		lower := strings.ToLower(err.Error())
		state := proto.NetbirdStopped
		if strings.Contains(lower, "run up command") || strings.Contains(lower, "setup-key") {
			state = proto.NetbirdNotLogin
		}

		rsp.OkRspWithData(c, &proto.GetNetbirdStatusRsp{
			State:   state,
			Version: getInstalledVersion(),
		})
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
