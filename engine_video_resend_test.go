package meowcaller

import (
	"bytes"
	"testing"
)

type recordingRelaySender struct{ sent [][]byte }

func (r *recordingRelaySender) Send(data []byte) (int, error) {
	r.sent = append(r.sent, append([]byte(nil), data...))
	return len(data), nil
}

func TestVideoSenderResendsTheIdenticalPacketsANackNames(t *testing.T) {
	out := &recordingRelaySender{}
	vs := &videoSender{ch: out}
	packet := func(seq uint16) []byte { return []byte{0x80, 97, byte(seq >> 8), byte(seq), 0xaa, byte(seq)} }
	for seq := uint16(65530); seq != 10; seq++ {
		vs.remember(packet(seq))
	}
	if n := vs.resend([]uint16{65534, 3, 4000}); n != 2 {
		t.Fatalf("resent %d, want 2 (4000 was never sent)", n)
	}
	if !bytes.Equal(out.sent[0], packet(65534)) || !bytes.Equal(out.sent[1], packet(3)) {
		t.Fatal("resent packets differ from the originals")
	}
	// A slot overwritten by a newer packet is not answered with the wrong one.
	vs.remember(packet(3 + videoHistorySize))
	if n := vs.resend([]uint16{3}); n != 0 {
		t.Fatal("answered a NACK with a packet that took its slot")
	}
}
