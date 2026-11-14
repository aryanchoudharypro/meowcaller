package rtp

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func TestParseRtcpFeedbackReadsNackAndRemb(t *testing.T) {
	// Generic NACK for SSRC 0x1234: packet 100 plus 101 and 103 from the bitmask.
	nack := make([]byte, 16)
	nack[0], nack[1] = 0x80|1, 205
	binary.BigEndian.PutUint16(nack[2:4], 3)
	binary.BigEndian.PutUint32(nack[4:8], 1)
	binary.BigEndian.PutUint32(nack[8:12], 0x1234)
	binary.BigEndian.PutUint16(nack[12:14], 100)
	binary.BigEndian.PutUint16(nack[14:16], 0b101)
	// REMB of 1.5 Mbit/s (mantissa 187500, exponent 3) covering SSRC 0x1234.
	remb := make([]byte, 24)
	remb[0], remb[1] = 0x80|15, 206
	binary.BigEndian.PutUint16(remb[2:4], 5)
	copy(remb[12:16], "REMB")
	remb[16] = 1
	mantissa := uint32(187500)
	remb[17] = byte(3<<2) | byte(mantissa>>16)
	remb[18], remb[19] = byte(mantissa>>8), byte(mantissa)
	binary.BigEndian.PutUint32(remb[20:24], 0x1234)

	fb := ParseRtcpFeedback(append(nack, remb...))
	if want := []uint16{100, 101, 103}; !reflect.DeepEqual(fb.Nacked[0x1234], want) {
		t.Fatalf("nacked %v, want %v", fb.Nacked[0x1234], want)
	}
	if fb.RembBps != 1_500_000 || !reflect.DeepEqual(fb.RembSsrcs, []uint32{0x1234}) {
		t.Fatalf("remb %d for %v", fb.RembBps, fb.RembSsrcs)
	}
	if !reflect.DeepEqual(fb.Types, []string{"NACK", "AFB"}) {
		t.Fatalf("types %v", fb.Types)
	}
}

func TestParseRtcpFeedbackSurvivesTruncation(t *testing.T) {
	for n := 0; n < 30; n++ {
		ParseRtcpFeedback(make([]byte, n))
		b := make([]byte, n)
		if n > 1 {
			b[0], b[1] = 0x81, 205
		}
		if n > 3 {
			b[3] = 200
		}
		ParseRtcpFeedback(b)
	}
}
