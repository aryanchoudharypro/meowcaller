package p2p

import "testing"

func TestAllowedGate(t *testing.T) {
	on := VoipSettings{EnableWebP2P: true}
	cases := []struct {
		name                                  string
		group, protect, contact, caller, want bool
		settings                              VoipSettings
	}{
		{"open 1:1 caller", false, false, false, true, true, on},
		{"callee from contact", false, false, true, false, true, on},
		{"callee from non-contact", false, false, false, false, false, on},
		{"group call", true, false, true, true, false, on},
		{"protect ip on", false, true, true, true, false, on},
		{"server disable_p2p", false, false, true, true, false, VoipSettings{EnableWebP2P: true, DisableP2P: true}},
		{"server no enable_web_p2p", false, false, true, true, false, VoipSettings{}},
	}
	for _, c := range cases {
		if got := Allowed(c.group, c.protect, c.contact, c.caller, c.settings); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
