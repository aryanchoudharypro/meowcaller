package meowcaller

import "testing"

func TestAlignAudioFrameConcealsLostPacket(t *testing.T) {
	frame := make([]float32, SampleRate/50)
	for i := range frame {
		frame[i] = 0.5
	}
	aligned := alignAudioFrame(frame, 2*len(frame))
	if len(aligned) != 2*len(frame) {
		t.Fatalf("length %d, want %d", len(aligned), 2*len(frame))
	}
	if aligned[len(frame)] == 0 {
		t.Fatal("hole was left silent instead of concealed")
	}
	if aligned[len(aligned)-1] >= aligned[len(frame)] {
		t.Fatal("concealment did not fade out")
	}
}

func TestObservePeerLossSmooths(t *testing.T) {
	var s mediaStatsCounters
	s.observePeerLoss(51) // 20%
	if got := s.peerLossPercent.Load(); got != 10 {
		t.Fatalf("first report gave %d, want 10", got)
	}
	s.observePeerLoss(51)
	if got := s.peerLossPercent.Load(); got != 15 {
		t.Fatalf("second report gave %d, want 15", got)
	}
}

func TestStandardOpusDecoderFillsLostPacket(t *testing.T) {
	frames := loadFixtureFrames(t, "testdata/opus_silk_wb_60ms_frames.json")
	warmUpLibopus(frames)
	dec, err := newStandardOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	first, lead, ok := dec.decodeAt(frames[0], 1000)
	if !ok || lead != 0 || len(first) != 960 {
		t.Fatalf("first packet: ok=%v lead=%d len=%d", ok, lead, len(first))
	}
	// The next packet arrives one whole packet late: one packet was lost.
	second, lead, ok := dec.decodeAt(frames[2], 1000+2*960)
	if !ok || lead != 960 || len(second) != 2*960 {
		t.Fatalf("after loss: ok=%v lead=%d len=%d, want a recovered packet in front", ok, lead, len(second))
	}
}

// A packet that only looks like Opus can trip one of libopus's assertions.
// That has to cost the packet, not the call.
func TestStandardOpusDecoderSurvivesAPacketThatTripsLibopus(t *testing.T) {
	dec, err := newStandardOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	junk := []byte{0x58, 0x01, 0x02, 0x03, 0x04}
	dec.decodeAt(junk, 1000)
	dec.decodeAt(junk, 1000+2*960) // tripped an assertion before the guard
	frames := loadFixtureFrames(t, "testdata/opus_silk_wb_60ms_frames.json")
	if pcm, _, ok := dec.decodeAt(frames[3], 50000); !ok || len(pcm) != 960 {
		t.Fatalf("the decoder did not recover: ok=%v len=%d", ok, len(pcm))
	}
}

// warmUpLibopus spends the process's first Opus decode. In the cgo-free libopus
// port the very first decode in a process trips a SILK resampler assertion
// whatever the packet (every later one is fine); decodeAt survives it by
// dropping that packet, which a test asserting on exact output cannot.
func warmUpLibopus(frames [][]byte) {
	if dec, err := newStandardOpusDecoder(); err == nil {
		dec.decodeAt(frames[1], 0)
	}
}
