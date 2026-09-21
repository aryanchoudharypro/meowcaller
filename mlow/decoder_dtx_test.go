package mlow

import (
	"encoding/hex"
	"math"
	"testing"
)

// TestDtxOffFramesDecodeToAudio pins the DTX-off decode path. A VoA=00 packet
// (TOC 0x10 here) is a normal frame carrying background noise, not a SID: the
// reference decodes it, and with DTX off a peer sends nothing else during a
// pause. Silencing it drops roughly an eighth of a real stream on the floor
// while the call merely sounds quiet.
//
// The fixture is the DTX-off capture from
// https://github.com/oxidezap/whatsapp-rust/pull/1113. Their reference PCM is
// not carried here, so this asserts the property that distinguishes the bug
// from the fix — those frames carry energy — rather than sample equality.
func TestDtxOffFramesDecodeToAudio(t *testing.T) {
	var frames []string
	loadJSON(t, "mlow_dtx_off_frames.json", &frames)

	dec := NewMlowDecoder()
	var out []float32
	var spans [][2]int
	for _, hf := range frames {
		fb, err := hex.DecodeString(hf)
		if err != nil {
			t.Fatalf("bad hex frame: %v", err)
		}
		start := len(out)
		out = append(out, dec.Decode(fb)...)
		// Not SID, VoA=0 and hang-over clear, i.e. coded inactive. 0x12 shares
		// VoA=0 but sets the hang-over bit, which makes it active and already
		// decoded before this change, so it must not be counted here.
		if fb[0]&0xC2 == 0 {
			spans = append(spans, [2]int{start, len(out)})
		}
	}
	if len(spans) == 0 {
		t.Fatal("fixture lost its DTX-off frames: this path is no longer covered")
	}
	if want := len(frames) * opusFrameSamps; len(out) != want {
		t.Fatalf("decoded %d samples, want %d (every fixture frame is 60 ms on-point)", len(out), want)
	}

	var energy float64
	var n int
	for _, s := range spans {
		for _, v := range out[s[0]:s[1]] {
			energy += float64(v) * float64(v)
		}
		n += s[1] - s[0]
	}
	rms := math.Sqrt(energy / float64(n))
	t.Logf("%d DTX-off frames (%d samples), rms=%.5f", len(spans), n, rms)
	if rms <= 0.0001 {
		t.Fatalf("DTX-off frames decoded to silence (rms=%.6f): they are being dropped, not decoded", rms)
	}
}
