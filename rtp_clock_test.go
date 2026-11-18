package meowcaller

import "testing"

// A 60 ms SILK wideband packet (TOC config 11, one frame).
var sixtyMsOpus = []byte{0x58, 1, 2, 3}

func TestRTPClockScalerRescalesA48kHzClock(t *testing.T) {
	var s rtpClockScaler
	ts := uint32(0xffff0000) // wraps during the run
	var out []uint32
	for i := 0; i < 20; i++ {
		out = append(out, s.normalize(sixtyMsOpus, uint16(100+i), ts, true))
		ts += 2880
	}
	if s.clockRate() != 48000 {
		t.Fatalf("clock %d, want 48000", s.clockRate())
	}
	// Before the clock settles every step is the raw 2880; after it 960, and
	// the output never jumps back.
	for i := 1; i < len(out); i++ {
		step := out[i] - out[i-1]
		if step != 2880 && step != 960 {
			t.Fatalf("step %d at %d", step, i)
		}
		if i > rtpClockVotes+1 && step != 960 {
			t.Fatalf("step %d at %d after settling", step, i)
		}
	}
	// A late packet lands behind the running count.
	late := s.normalize(sixtyMsOpus, 110, ts-3*2880, true) // two packets behind the last
	if want := out[len(out)-1] - 2*960; late != want {
		t.Fatalf("late %d, want %d", late, want)
	}
}

func TestRTPClockScaler24kHzAndOff(t *testing.T) {
	var s rtpClockScaler
	ts := uint32(5000)
	for i := 0; i < 10; i++ {
		s.normalize(sixtyMsOpus, uint16(i), ts, true)
		ts += 1440
	}
	if s.clockRate() != 24000 {
		t.Fatalf("clock %d, want 24000", s.clockRate())
	}
	// Not enabled: a 48 kHz-looking stream is passed through untouched.
	var off rtpClockScaler
	ts = 7
	for i := 0; i < 10; i++ {
		if got := off.normalize(sixtyMsOpus, uint16(i), ts, false); got != ts {
			t.Fatalf("got %d, want %d", got, ts)
		}
		ts += 2880
	}
	// A 16 kHz stream stays 16 kHz even when measured.
	var wb rtpClockScaler
	ts = 7
	for i := 0; i < 10; i++ {
		if got := wb.normalize(sixtyMsOpus, uint16(i), ts, true); got != ts {
			t.Fatalf("got %d, want %d", got, ts)
		}
		ts += 960
	}
}
