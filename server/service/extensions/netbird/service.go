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

// claim guards every handler that can bring a VPN client up. Two things are
// checked, and both matter:
//
//   - the device may run only one VPN client (161 MB of RAM), so a client may
//     start only when it is the selected one;
//   - starting and stopping must not interleave with a concurrent switch.
//
// This lives in the HTTP layer on purpose. It must NOT move into Cli.Start():
// SetPreference calls Cli.Start() while /etc/kvm/vpn still holds the previous
// value, so a check down there would make switching — and its rollback —
// impossible forever.
func claim(c *gin.Context, rsp *proto.Response) bool {
	if vpnpref.Read() != vpnpref.Netbird {
		rsp.ErrRsp(c, -6, "NetBird is not the selected VPN: enable NetBird autostart first")
		return false
	}

	if !vpnpref.TryLock() {
		rsp.ErrRsp(c, -5, "another VPN operation is in progress, please retry")
		return false
	}

	return true
}

func (s *Service) Install(c *gin.Context) {
	var rsp proto.Response

	// Deliberately not behind claim(): the preference is not required to install.
	// A tunnel is raised afterwards only when NetBird is already the selected VPN;
	// requiring the preference up front would be a trap. On a fresh device the
	// preference is tailscale and the autostart switch is hidden until NetBird is
	// installed, so a preference check would make NetBird impossible to install
	// at all. The daemon is started only once NetBird is the selected VPN.
	if !vpnpref.TryLock() {
		rsp.ErrRsp(c, -5, "another VPN operation is in progress, please retry")
		return
	}
	defer vpnpref.Unlock()

	if err := install(true); err != nil {
		rsp.ErrRsp(c, -1, fmt.Sprintf("install failed: %v", err))
		log.Errorf("failed to install netbird: %s", err)
		return
	}

	if vpnpref.Read() != vpnpref.Netbird {
		// Installed, but Tailscale still owns autostart. Leave it that way: the
		// user switches over with the autostart toggle, which now becomes visible.
		rsp.OkRsp(c)
		return
	}

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

	if !claim(c, &rsp) {
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

	if !claim(c, &rsp) {
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
	if !claim(c, &rsp) {
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
