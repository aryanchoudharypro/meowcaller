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
