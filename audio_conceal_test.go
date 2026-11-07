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
	dec, err := newStandardOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	// 60 ms SILK wideband packets as libopus makes them: a TOC byte and a body.
	packet := []byte{0x58, 0x01, 0x02, 0x03, 0x04}
	first, lead, ok := dec.decodeAt(packet, 1000)
	if !ok || lead != 0 || len(first) != 960 {
		t.Fatalf("first packet: ok=%v lead=%d len=%d", ok, lead, len(first))
	}
	// The next packet arrives one whole packet late: one packet was lost.
	second, lead, ok := dec.decodeAt(packet, 1000+2*960)
	if !ok || lead != 960 || len(second) != 2*960 {
		t.Fatalf("after loss: ok=%v lead=%d len=%d, want a recovered packet in front", ok, lead, len(second))
	}
}
