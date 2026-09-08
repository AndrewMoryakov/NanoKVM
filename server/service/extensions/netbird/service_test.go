package netbird

import (
	"NanoKVM-Server/service/extensions/vpnpref"
	"context"
	"testing"
	"time"
)

func TestInvalidateStagedInstallCancelsOlderIntent(t *testing.T) {
	// Ensure this package-global coordinator starts from an inactive state even
	// if a previous test was interrupted before its cleanup.
	vpnpref.InvalidateStagedInstalls()
	ctx, generation, finish := vpnpref.BeginStagedInstall(context.Background())
	defer finish()

	if !vpnpref.StagedInstallCurrent(generation) {
		t.Fatal("new staged install is not current")
	}
	vpnpref.InvalidateStagedInstalls()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("newer lifecycle action did not cancel staged install")
	}
	if vpnpref.StagedInstallCurrent(generation) {
		t.Fatal("invalidated staged install remained current")
	}
}

func TestCrossVPNLifecycleActionCancelsStagedInstall(t *testing.T) {
	// Staging deliberately occurs without the lifecycle lock. Model a newer
	// Tailscale or SetPreference operation that obtains that lock while the
	// download is pending; its shared coordinator invalidation must make the
	// older stage ineligible for promotion.
	vpnpref.InvalidateStagedInstalls()
	ctx, generation, finish := vpnpref.BeginStagedInstall(context.Background())
	defer finish()

	if !vpnpref.TryLock() {
		t.Fatal("failed to acquire VPN lifecycle lock")
	}
	vpnpref.InvalidateStagedInstalls()
	vpnpref.Unlock()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cross-VPN lifecycle action did not cancel staged install")
	}
	if vpnpref.StagedInstallCurrent(generation) {
		t.Fatal("cross-VPN lifecycle action left staged install current")
	}
}
