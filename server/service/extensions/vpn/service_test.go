package vpn

import (
	"NanoKVM-Server/service/extensions/vpnpref"
	"errors"
	"testing"
)

func TestRunningTailscaleUsesDaemonProbeAndFailsClosed(t *testing.T) {
	original := tailscaleServiceRunning
	t.Cleanup(func() { tailscaleServiceRunning = original })

	tailscaleServiceRunning = func() (bool, error) { return true, nil }
	if !running(vpnpref.Tailscale) {
		t.Fatal("running(tailscale) = false with a verified daemon")
	}

	tailscaleServiceRunning = func() (bool, error) { return false, nil }
	if running(vpnpref.Tailscale) {
		t.Fatal("running(tailscale) = true with a verified absent daemon")
	}

	tailscaleServiceRunning = func() (bool, error) { return false, errors.New("temporary proc failure") }
	if !running(vpnpref.Tailscale) {
		t.Fatal("running(tailscale) = false on uncertain daemon state; must fail closed")
	}
}

func TestBootableNetbirdUsesPinAwareEligibility(t *testing.T) {
	original := netbirdCanStartAtBoot
	t.Cleanup(func() { netbirdCanStartAtBoot = original })

	netbirdCanStartAtBoot = func() bool { return false }
	if bootable(vpnpref.Netbird) {
		t.Fatal("bootable(netbird) accepted a NetBird client rejected by its pin-aware eligibility check")
	}

	netbirdCanStartAtBoot = func() bool { return true }
	if !bootable(vpnpref.Netbird) {
		t.Fatal("bootable(netbird) ignored a NetBird client accepted by its eligibility check")
	}
}
