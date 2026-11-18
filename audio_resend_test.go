package meowcaller

import (
	"encoding/binary"
	"testing"
)

func audioPacket(seq uint16) []byte {
	p := make([]byte, 20)
	p[0] = 0x80
	binary.BigEndian.PutUint16(p[2:4], seq)
	return p
}

func TestAudioNackIsAnsweredFromHistory(t *testing.T) {
	var h audioResendHistory
	for seq := uint16(65530); seq != 10; seq++ {
		h.remember(audioPacket(seq))
	}
	var sent []uint16
	send := func(p []byte) (int, error) {
		sent = append(sent, binary.BigEndian.Uint16(p[2:4]))
		return len(p), nil
	}
	if n := h.resend([]uint16{65533, 2, 500}, 0, send); n != 2 {
		t.Fatalf("resent %d, want 2 (500 was never sent)", n)
	}
	if sent[0] != 65533 || sent[1] != 2 {
		t.Fatalf("resent %v", sent)
	}
	for i := 0; i < 5; i++ {
		h.resend([]uint16{2}, 0, send)
	}
	if got := len(sent) - 1; got != defaultAudioResends {
		t.Fatalf("packet 2 resent %d times, want at most %d", got, defaultAudioResends)
	}
	h.remember(audioPacket(2 + audioHistorySize))
	if n := h.resend([]uint16{2}, 0, send); n != 0 {
		t.Fatal("resent a packet whose slot was reused")
	}
}
