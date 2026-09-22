// Package vpnpref stores which VPN client is allowed to run on this device.
//
// The device has too little RAM for two VPN clients at once, so exactly one of
// them may be active. The preference is read at boot by S95nanokvm and enforced
// by the HTTP handlers that can start a client.
//
// This package must not import the tailscale or netbird packages: they import it.
package vpnpref

import (
	"os"
	"strings"
	"sync"
)

const (
	File = "/etc/kvm/vpn"

	Tailscale = "tailscale"
	Netbird   = "netbird"
)

// Read returns the preferred VPN, defaulting to tailscale when the file is
// missing or holds anything unexpected.
func Read() string {
	data, err := os.ReadFile(File)
	if err != nil {
		return Tailscale
	}

	if strings.TrimSpace(string(data)) == Netbird {
		return Netbird
	}

	return Tailscale
}

// Write records the preferred VPN. Callers must only do this after the new
// client has actually started, so the file never runs ahead of reality.
func Write(vpn string) error {
	return os.WriteFile(File, []byte(vpn), 0o644)
}

// IsValid reports whether vpn names a VPN this device knows about.
func IsValid(vpn string) bool {
	return vpn == Tailscale || vpn == Netbird
}

// mu serializes every operation that starts or stops a VPN client. Without it
// two concurrent requests interleave stop/start and can leave the device with
// no VPN at all — or, worse, with both running.
var mu sync.Mutex

// TryLock reports whether the caller now owns the VPN state. A caller that gets
// false must fail fast rather than wait: the client times out at 60s and the
// operations behind this lock can take longer.
func TryLock() bool {
	return mu.TryLock()
}

func Unlock() {
	mu.Unlock()
}
