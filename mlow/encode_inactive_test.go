package mlow

import (
	"math"
	"testing"
)

// speechSilenceSpeech is 1 s of a speech-like tone, 3 s of a -60 dBFS
// comfort-noise floor, then 1 s of tone again.
func speechSilenceSpeech() []float32 {
	seed := uint32(0x12345678)
	noise := func() float32 {
		seed = seed*1664525 + 1013904223
		return float32(seed>>8)/8388608.0 - 1.0
	}
	tone := func(i int) float32 {
		t := float64(i) / 16000.0
		return float32(0.25*math.Sin(2*math.Pi*150*t) + 0.05*math.Sin(2*math.Pi*1700*t))
	}
	sig := make([]float32, 0, 80000)
	for i := 0; i < 16000; i++ {
		sig = append(sig, tone(i))
	}
	for i := 0; i < 48000; i++ {
		sig = append(sig, 0.001*noise())
	}
	for i := 0; i < 16000; i++ {
		sig = append(sig, tone(i))
	}
	return sig
}

func encodeAll(t *testing.T, enc *MlowEncoder, pcm []float32) [][]byte {
	t.Helper()
	var frames [][]byte
	for off := 0; off+960 <= len(pcm); off += 960 {
		frame, err := enc.Encode(pcm[off : off+960])
		if err != nil {
			t.Fatalf("encode at sample %d: %v", off, err)
		}
		frames = append(frames, frame)
	}
	return frames
}

// TestSilenceIsCodedInactiveAndSpeechIsNot: a receiver has to be able to tell
// the sender's background noise apart from its speech. The reference emits 0x10
// over silence with DTX off, 0x12 for the hangover packet after a talkspurt.
func TestSilenceIsCodedInactiveAndSpeechIsNot(t *testing.T) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/99a9a118/wacore/src/voip/mlow/encode.rs#L798-L845
	frames := encodeAll(t, NewMlowEncoder(), speechSilenceSpeech())
	tocs := make([]byte, len(frames))
	for i, f := range frames {
		tocs[i] = f[0]
		if f[0] != 0x50 && f[0] != 0x12 && f[0] != 0x10 {
			t.Fatalf("frame %d: non-config-0 60 ms TOC %#02x", i, f[0])
		}
	}
	run := func(from int, want byte) int {
		n := 0
		for _, toc := range tocs[from:] {
			if toc != want {
				break
			}
			n++
		}
		return n
	}
	if run(0, 0x50) < 14 {
		t.Fatalf("the whole first talkspurt is active voice: %x", tocs)
	}
	if run(36, 0x10) < 25 {
		t.Fatalf("a settled comfort-noise floor stays coded inactive: %x", tocs)
	}
	lastInactive := -1
	hangover := false
	for i, toc := range tocs {
		if toc == 0x10 {
			lastInactive = i
		}
		if toc == 0x12 {
			hangover = true
		}
	}
	if lastInactive < 0 || run(lastInactive+1, 0x50) < 14 {
		t.Fatalf("speech after the silence is active voice again: %x", tocs)
	}
	if !hangover {
		t.Fatalf("the hangover packet after a talkspurt is coded active with the VAD bit clear: %x", tocs)
	}

	zeros := encodeAll(t, NewMlowEncoder(), make([]float32, 960*20))
	for i, f := range zeros[1:] {
		if f[0] != 0x10 {
			t.Fatalf("digital silence frame %d has TOC %#02x, want 0x10", i+1, f[0])
		}
	}
}

// TestInactiveFramesDecodeWithoutDesync: an inactive frame leaves two LSF
// symbols off the wire, so a writer that still emits them (or a reader that
// still consumes them) desyncs the range coder and poisons the cross-frame
// predictor. The canary is the talkspurt AFTER the silence.
func TestInactiveFramesDecodeWithoutDesync(t *testing.T) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/99a9a118/wacore/src/voip/mlow/encode.rs#L847-L895
	frames := encodeAll(t, NewMlowEncoder(), speechSilenceSpeech())
	dec := NewMlowDecoder()
	levels := make([]float64, 0, len(frames))
	for i, f := range frames {
		pcm := dec.Decode(f)
		if len(pcm) != 960 {
			t.Fatalf("frame %d decoded to %d samples, want 960", i, len(pcm))
		}
		var sum float64
		for _, s := range pcm {
			if math.IsNaN(float64(s)) || math.IsInf(float64(s), 0) {
				t.Fatalf("frame %d: non-finite output", i)
			}
			sum += float64(s) * float64(s)
		}
		levels = append(levels, 20*math.Log10(math.Sqrt(sum/float64(len(pcm)))+1e-12))
	}
	before, silence, after := levels[8], levels[40], levels[75]
	if before <= -25 || after <= -25 {
		t.Fatalf("both talkspurts must decode at speech level: %.1f then %.1f dBFS", before, after)
	}
	if math.Abs(after-before) >= 6 {
		t.Fatalf("the talkspurt after the inactive frames must decode like the one before it (%.1f vs %.1f dBFS)", before, after)
	}
	if silence >= before-20 {
		t.Fatalf("the inactive stretch must stay a floor (%.1f dBFS)", silence)
	}
}
