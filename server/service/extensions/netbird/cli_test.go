package netbird

import "testing"

func TestParseStatusAcceptsOnlyJSONObservation(t *testing.T) {
	status, err := parseStatus("warning\n{\"fqdn\":\"nano\",\"management\":{\"connected\":true},\"signal\":{\"connected\":true}}")
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	if status.FQDN != "nano" || !status.Management.Connected || !status.Signal.Connected {
		t.Fatalf("unexpected parsed status: %#v", status)
	}

	if _, err := parseStatus("netbird daemon timed out"); err == nil {
		t.Fatal("parseStatus accepted a non-JSON diagnostic")
	}
}

func TestIsNetbirdDaemonCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmdline []byte
		want    bool
	}{
		{"daemon", []byte("/usr/bin/netbird\x00service\x00run\x00"), true},
		{"status client", []byte("/usr/bin/netbird\x00status\x00--json\x00"), false},
		{"login client", []byte("/usr/bin/netbird\x00up\x00--no-browser\x00"), false},
		{"missing arguments", []byte("/usr/bin/netbird\x00"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isNetbirdDaemonCommand(test.cmdline); got != test.want {
				t.Fatalf("isNetbirdDaemonCommand() = %t, want %t", got, test.want)
			}
		})
	}
}
