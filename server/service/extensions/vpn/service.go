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

	// Stop the old client before starting the new one: the device cannot host
	// both at once. If the new one fails to start, put the old one back — this
	// device is usually reached through the very tunnel being switched.
	if err := switchTo(vpn); err != nil {
		rsp.ErrRsp(c, -3, err.Error())
		return
	}

	// Written last, so the file never claims a state the device is not in.
	if err := vpnpref.Write(vpn); err != nil {
		log.Errorf("failed to write VPN preference: %s", err)
		rsp.ErrRsp(c, -4, fmt.Sprintf("write preference failed: %v", err))
		return
	}

	log.Infof("VPN preference set to %s", vpn)
	rsp.OkRspWithData(c, &proto.GetVPNPreferenceRsp{VPN: vpn})
}

// stopper is the subset of a VPN cli needed to roll a failed switch back.
type stopper interface {
	Start() error
	Stop() error
}

func switchTo(vpn string) error {
	var old, target stopper
	if vpn == vpnpref.Netbird {
		old, target = tailscale.NewCli(), netbird.NewCli()
	} else {
		old, target = netbird.NewCli(), tailscale.NewCli()
	}

	// A failed stop must abort the switch. S99netbird now confirms the daemon is
	// actually gone before reporting success — signal senders like
	// start-stop-daemon -K return 0 as soon as the signal is sent, which says
	// nothing about whether the process died. Starting the new client on top of
	// a live old one is the both-VPNs state this whole design exists to prevent.
	//
	// Strength differs by direction. Stopping NetBird is verified: S99netbird
	// polls until the process is gone. Stopping Tailscale is not: S98tailscaled
	// exits 0 from its stop branch whatever happens, so a daemon that ignored the
	// signal looks like success here. Fixing that script is out of scope.
	if err := old.Stop(); err != nil {
		return fmt.Errorf("could not confirm the current VPN stopped, so %s was not started: %w", vpn, err)
	}

	if err := target.Start(); err != nil {
		log.Errorf("failed to start %s: %s", vpn, err)

		if rbErr := old.Start(); rbErr != nil {
			return fmt.Errorf("start %s failed: %v; rollback failed: %v", vpn, err, rbErr)
		}

		// S98tailscaled exits 0 even when the daemon did not come up, so a
		// successful return here is not proof the tunnel is back. Say what we
		// know, not what we hope.
		return fmt.Errorf("start %s failed: %v; rollback attempted", vpn, err)
	}

	return nil
}
