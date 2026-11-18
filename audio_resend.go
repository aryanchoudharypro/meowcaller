package meowcaller

import (
	"encoding/binary"
	"sync"
)

// audioHistorySize is how many of our sent audio packets are kept for a NACK:
// 64 packets of 60 ms, about four seconds.
const audioHistorySize = 64

// defaultAudioResends is how often one audio packet is sent again at most when
// the server's settings do not say (options.max_rtp_audio_packet_resends is 3
// in every blob of the user's logs).
const defaultAudioResends = 3

// audioResendHistory keeps our last protected audio packets by sequence number
// so a NACK from the peer or the relay is answered with the identical packet.
type audioResendHistory struct {
	mu      sync.Mutex
	packets [audioHistorySize][]byte
	seqs    [audioHistorySize]uint16
	resends [audioHistorySize]uint8
}

// remember stores a protected RTP packet just sent.
func (h *audioResendHistory) remember(packet []byte) {
	// Source of truth: WA Web VoIP wasm rEjXwUYzMaj (enable_hbh_nack_audio, max_rtp_audio_packet_resends)
	if len(packet) < 12 {
		return
	}
	seq := binary.BigEndian.Uint16(packet[2:4])
	slot := int(seq) % audioHistorySize
	h.mu.Lock()
	h.packets[slot], h.seqs[slot], h.resends[slot] = packet, seq, 0
	h.mu.Unlock()
}

// resend sends each still-held packet a NACK names again through send, at most
// maxResends times per packet, and returns how many went out.
func (h *audioResendHistory) resend(seqs []uint16, maxResends int, send func([]byte) (int, error)) int {
	// Source of truth: WA Web VoIP wasm rEjXwUYzMaj (max_rtp_audio_packet_resends)
	if maxResends <= 0 {
		maxResends = defaultAudioResends
	}
	var due [][]byte
	h.mu.Lock()
	for _, seq := range seqs {
		slot := int(seq) % audioHistorySize
		if h.packets[slot] == nil || h.seqs[slot] != seq || int(h.resends[slot]) >= maxResends {
			continue
		}
		h.resends[slot]++
		due = append(due, h.packets[slot])
	}
	h.mu.Unlock()
	n := 0
	for _, packet := range due {
		if _, err := send(packet); err == nil {
			n++
		}
	}
	return n
}
