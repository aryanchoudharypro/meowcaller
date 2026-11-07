package rtp

import "testing"

func TestParseReceptionFractionLost(t *testing.T) {
	var cname [WhatsappRtcpCnameLen]byte
	report := &RtcpReceptionReport{Ssrc: 0xAABBCCDD, FractionLost: 26}
	packet := BuildSenderReportWithSdesAndReception(7, &RtcpSenderStats{}, 1_000, &cname, report, false)
	got, ok := ParseReceptionFractionLost(packet, 0xAABBCCDD)
	if !ok || got != 26 {
		t.Fatalf("got %d ok=%v, want 26 true", got, ok)
	}
	if _, ok := ParseReceptionFractionLost(packet, 1); ok {
		t.Fatal("matched a block for another SSRC")
	}
	if _, ok := ParseReceptionFractionLost([]byte{1, 2, 3}, 1); ok {
		t.Fatal("matched a malformed packet")
	}
}
