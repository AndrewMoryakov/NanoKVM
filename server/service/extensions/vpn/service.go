package vpn

import (
	"NanoKVM-Server/proto"
	"NanoKVM-Server/service/extensions/netbird"
	"NanoKVM-Server/service/extensions/tailscale"
	"NanoKVM-Server/service/extensions/vpnpref"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type Service struct{}

func NewService() *Service {
	return &Service{}
}

func (s *Service) GetPreference(c *gin.Context) {
	var rsp proto.Response

	rsp.OkRspWithData(c, &proto.GetVPNPreferenceRsp{VPN: vpnpref.Read()})
}

// SetPreference records which client autostarts, and — only when that is safe —
// stops the other one.
//
// The preference governs boot. It is not a permission to run: both clients can be
// started at any time, and the daemons coexist (only one tunnel is up, and the
// memory cost is transient). That separation exists because of one scenario: this
// device is normally reached *through* the VPN being switched away from. The old
// design stopped the current client first and started the new one, which on a
// remote device meant the session died before the incoming client was usable —
// and NetBird's first login is interactive, so it was never usable in time. The
// device could not be recovered without physical or LAN access.
//
// So the rule is: never stop a working tunnel until the incoming one is proven
// connected. If nothing is running there is nothing to lose, and the preference
// is recorded without touching either client.
func (s *Service) SetPreference(c *gin.Context) {
	var req proto.SetVPNPreferenceReq
	var rsp proto.Response

	if err := proto.ParseFormRequest(c, &req); err != nil {
		rsp.ErrRsp(c, -1, "invalid parameters")
		return
	}

	vpn := strings.TrimSpace(req.VPN)
	if !vpnpref.IsValid(vpn) {
		rsp.ErrRsp(c, -2, "vpn must be 'tailscale' or 'netbird'")
		return
	}

	if vpn == vpnpref.Read() {
		rsp.OkRspWithData(c, &proto.GetVPNPreferenceRsp{VPN: vpn})
		return
	}

	if !vpnpref.TryLock() {
		rsp.ErrRsp(c, -5, "another VPN operation is in progress, please retry")
		return
	}
	defer vpnpref.Unlock()

	other := otherVPN(vpn)

	if running(other) {
		if !connected(vpn) {
			rsp.ErrRsp(c, -6, fmt.Sprintf(
				"%s is not connected yet — start it and finish signing in before making it the autostart VPN, "+
					"otherwise stopping %s now would cut the connection you are using", vpn, other))
			return
		}

		if err := cliFor(other).Stop(); err != nil {
			log.Errorf("failed to stop %s: %s", other, err)
			rsp.ErrRsp(c, -3, fmt.Sprintf("%s is connected, but %s could not be stopped: %v", vpn, other, err))
			return
		}
	}

	// Written last, so the file never claims a state the device is not in.
	if err := vpnpref.Write(vpn); err != nil {
		log.Errorf("failed to write VPN preference: %s", err)
		rsp.ErrRsp(c, -4, fmt.Sprintf("write preference failed: %v", err))
		return
	}

	log.Infof("VPN autostart set to %s", vpn)
	rsp.OkRspWithData(c, &proto.GetVPNPreferenceRsp{VPN: vpn})
}

type stopper interface {
	Stop() error
}

func otherVPN(vpn string) string {
	if vpn == vpnpref.Netbird {
		return vpnpref.Tailscale
	}

	return vpnpref.Netbird
}

func cliFor(vpn string) stopper {
	if vpn == vpnpref.Netbird {
		return netbird.NewCli()
	}

	return tailscale.NewCli()
}

// running reports whether the client's daemon is up. A client that is not
// running cannot be cut off, so switching away from it is always safe.
func running(vpn string) bool {
	if vpn == vpnpref.Netbird {
		isRunning, err := netbird.NewCli().ServiceRunning()
		return err == nil && isRunning
	}

	// Tailscale has no equivalent service check; a readable status means the
	// daemon answered.
	_, err := tailscale.NewCli().Status()
	return err == nil
}

// connected reports whether the client actually carries a tunnel right now —
// not merely that its daemon is alive. This is the gate that prevents a remote
// lockout, so it is deliberately strict: anything short of a confirmed
// connection counts as not connected.
func connected(vpn string) bool {
	if vpn == vpnpref.Netbird {
		status, err := netbird.NewCli().Status()
		if err != nil {
			return false
		}

		return status.Management.Connected && status.Signal.Connected
	}

	status, err := tailscale.NewCli().Status()
	if err != nil {
		return false
	}

	return status.BackendState == "Running"
}
