package meowcaller

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
)

func TestIsSilentLightweightOffer(t *testing.T) {
	silence := waBinary.Node{Tag: "silence", Attrs: waBinary.Attrs{"reason": "vc_wave_all"}}
	cases := []struct {
		name string
		node *waBinary.Node
		want bool
	}{
		{"silent lightweight", &waBinary.Node{Tag: "offer", Attrs: waBinary.Attrs{"lightweight": "1"}, Content: []waBinary.Node{silence}}, true},
		{"lightweight that rings", &waBinary.Node{Tag: "offer", Attrs: waBinary.Attrs{"lightweight": "1"}}, false},
		{"normal offer", &waBinary.Node{Tag: "offer", Attrs: waBinary.Attrs{}, Content: []waBinary.Node{silence}}, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := isSilentLightweightOffer(c.node); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
