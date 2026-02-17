package vpn

import (
	"NanoKVM-Server/proto"
	"NanoKVM-Server/service/extensions/netbird"
	"NanoKVM-Server/service/extensions/tailscale"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

const VPNPreferenceFile = "/etc/kvm/vpn"

type Service struct{}

func NewService() *Service {
	return &Service{}
}

func (s *Service) GetPreference(c *gin.Context) {
	var rsp proto.Response

	vpn := readPreference()
	rsp.OkRspWithData(c, &proto.GetVPNPreferenceRsp{VPN: vpn})
}

func (s *Service) SetPreference(c *gin.Context) {
	var req proto.SetVPNPreferenceReq
	var rsp proto.Response

	if err := proto.ParseFormRequest(c, &req); err != nil {
		rsp.ErrRsp(c, -1, "invalid parameters")
		return
	}

	vpn := strings.TrimSpace(req.VPN)
	if vpn != "" && vpn != "tailscale" && vpn != "netbird" {
		rsp.ErrRsp(c, -2, "vpn must be 'tailscale', 'netbird' or empty")
		return
	}

	current := readPreference()
	if vpn == current {
		rsp.OkRspWithData(c, &proto.GetVPNPreferenceRsp{VPN: vpn})
		return
	}

	// Stop the old VPN and start the new one
	switch vpn {
	case "tailscale":
		_ = netbird.NewCli().Stop()
		time.Sleep(2 * time.Second)
		if err := tailscale.NewCli().Start(); err != nil {
			log.Errorf("failed to start tailscale: %s", err)
			rsp.ErrRsp(c, -3, fmt.Sprintf("start tailscale failed: %v", err))
			return
		}
	case "netbird":
		_ = tailscale.NewCli().Stop()
		time.Sleep(2 * time.Second)
		if err := netbird.NewCli().Start(); err != nil {
			log.Errorf("failed to start netbird: %s", err)
			rsp.ErrRsp(c, -3, fmt.Sprintf("start netbird failed: %v", err))
			return
		}
	case "":
		// Disable autostart — don't stop running VPNs
	}

	if err := writePreference(vpn); err != nil {
		log.Errorf("failed to write VPN preference: %s", err)
		rsp.ErrRsp(c, -4, fmt.Sprintf("write preference failed: %v", err))
		return
	}

	log.Infof("VPN preference set to %s", vpn)
	rsp.OkRspWithData(c, &proto.GetVPNPreferenceRsp{VPN: vpn})
}

func readPreference() string {
	data, err := os.ReadFile(VPNPreferenceFile)
	if err != nil {
		return ""
	}

	vpn := strings.TrimSpace(string(data))
	if vpn == "tailscale" || vpn == "netbird" {
		return vpn
	}

	return ""
}

func writePreference(vpn string) error {
	return os.WriteFile(VPNPreferenceFile, []byte(vpn), 0o644)
}
