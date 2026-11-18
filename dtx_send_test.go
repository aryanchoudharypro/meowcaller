package meowcaller

import (
	"testing"

	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/signaling"
)

func TestDTXSilenceIsLeftOutAfterTheStartWindow(t *testing.T) {
	c := &Call{}
	if skipDTXFrame(c, []byte{0x58}, 100) {
		t.Fatal("skipped a frame while the server had not asked for DTX")
	}
	c.dtx.Store(true)
	c.dtxStartMs.Store(3000)
	if skipDTXFrame(c, []byte{0x58}, 50) {
		t.Fatal("skipped a frame inside the 3 s start window")
	}
	if !skipDTXFrame(c, []byte{0x58}, 51) {
		t.Fatal("kept a DTX frame after the start window")
	}
	if skipDTXFrame(c, []byte{0x58, 1, 2, 3}, 51) {
		t.Fatal("skipped a frame that carries audio")
	}
	if skipDTXFrame(c, nil, 51) {
		t.Fatal("skipped a frame the source did not encode")
	}
}

func TestSkippedFrameMovesTheTimestampNotTheSequence(t *testing.T) {
	s := rtp.NewRtpStream(1, FrameSamples, false)
	first := s.NextPacket([]byte{0x58, 1, 2, 3}, false)
	s.SkipFrame()
	s.SkipFrame()
	next := s.NextPacket([]byte{0x58, 1, 2, 3}, false)
	if next.SequenceNumber != first.SequenceNumber+1 {
		t.Fatalf("sequence %d after %d, want contiguous", next.SequenceNumber, first.SequenceNumber)
	}
	if next.Timestamp-first.Timestamp != 3*FrameSamples {
		t.Fatalf("timestamp moved %d, want %d", next.Timestamp-first.Timestamp, 3*FrameSamples)
	}
}

func TestVoipSettingsReadDTX(t *testing.T) {
	vs, err := signaling.ParseVoipSettings([]byte(`{"rc":{"dtx":"1"},"options":{"enable_additional_dtx_frames_at_call_start_ms":"3000"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !vs.DTX || vs.DTXStartMs != 3000 {
		t.Fatalf("got dtx %v start %d", vs.DTX, vs.DTXStartMs)
	}
}
