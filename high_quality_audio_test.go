package meowcaller

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
)

// Every offer seen so far carries opus at 16000 and 8000: nothing to report.
// A higher rate is how a peer would offer more, and is reported.
func TestUnusualAudioRatesInAnOffer(t *testing.T) {
	audio := func(rate string) waBinary.Node {
		return waBinary.Node{Tag: "audio", Attrs: waBinary.Attrs{"enc": "opus", "rate": rate}}
	}
	today := waBinary.Node{Tag: "offer", Content: []waBinary.Node{audio("16000"), audio("8000")}}
	if got := unusualAudioRates(&today); len(got) != 0 {
		t.Fatalf("today's offer reported: %v", got)
	}
	someday := waBinary.Node{Tag: "offer", Content: []waBinary.Node{audio("48000"), audio("16000")}}
	if got := unusualAudioRates(&someday); len(got) != 1 || got[0] != "opus/48000" {
		t.Fatalf("got %v, want [opus/48000]", got)
	}
	if unusualAudioRates(nil) != nil {
		t.Fatal("nil node")
	}
}
