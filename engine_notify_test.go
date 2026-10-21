package meowcaller

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func TestOnlyParticipantStanzasCarryOurName(t *testing.T) {
	cases := []struct {
		to   any
		want bool
	}{
		{types.NewJID("31624565665834", types.HiddenUserServer), true},
		{"31624565665834@lid", true},
		{types.NewJID("919999999999", types.DefaultUserServer), true},
		{types.NewJID("004409F5B7B5D51E5B0270AA64B3D07D", "call"), false},
		{"004409F5B7B5D51E5B0270AA64B3D07D@call", false},
		{"call", false},
	}
	for _, c := range cases {
		node := waBinary.Node{Tag: "call", Attrs: waBinary.Attrs{"to": c.to}}
		if got := stanzaToParticipant(node); got != c.want {
			t.Errorf("to %v: got %v, want %v", c.to, got, c.want)
		}
	}
}
