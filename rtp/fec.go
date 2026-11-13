package rtp

import "encoding/binary"

const (
	// RtpPayloadTypeVideoFec is the payload type of WhatsApp's video FEC stream.
	RtpPayloadTypeVideoFec uint8 = 103

	fecRTPFixedLen = 12
	fecFixedLen    = 12
	fecStreamLen   = 6
	fecMaskBlocks  = 3
)

// fecMaskBlockBits is each mask block's width in mask bits, after its k bit.
var fecMaskBlockBits = [fecMaskBlocks]int{15, 31, 63}

// fecMaskLens is the mask length in bytes for one, two and three blocks.
var fecMaskLens = [fecMaskBlocks]int{2, 6, 14}

// FecHeader is a parsed FlexFEC-03 repair header for one protected stream, in
// WhatsApp's multistream layout.
type FecHeader struct {
	Padding       bool
	Extension     bool
	CSRCCount     uint8
	Marker        bool
	PayloadType   uint8
	Length        uint16
	Timestamp     uint32
	ProtectedSSRC uint32
	SequenceBase  uint16
	// Mask bit i set means the packet with sequence number SequenceBase+i is protected.
	MaskLow, MaskHigh uint64
}

// ParseFecHeader reads a repair header from the start of a repair payload and
// returns it with the header's length. ok is false for a retransmission (R=1), a
// fixed-offset mask (F=1), any number of protected streams but one, or a mask
// whose last k bit is missing.
func ParseFecHeader(data []byte) (FecHeader, int, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/fec.rs#L144-L200
	if len(data) < fecFixedLen+fecStreamLen {
		return FecHeader{}, 0, false
	}
	if data[0]&0xc0 != 0 || data[8] != 1 {
		return FecHeader{}, 0, false
	}
	offset := fecFixedLen + fecStreamLen
	blocks := 0
	for {
		if blocks == fecMaskBlocks {
			return FecHeader{}, 0, false
		}
		start := 0
		if blocks > 0 {
			start = fecMaskLens[blocks-1]
		}
		end := fecMaskLens[blocks]
		if len(data) < offset+end {
			return FecHeader{}, 0, false
		}
		blocks++
		// The k bit leads the block just read.
		if data[offset+start]&0x80 != 0 {
			break
		}
	}
	maskLen := fecMaskLens[blocks-1]
	// Walk the bit string back out, k bits skipped, mask bit 0 first.
	totalBits := maskLen * 8
	position := totalBits
	index := 0
	var maskLow, maskHigh uint64
	for block := 0; block < blocks; block++ {
		position-- // the k bit
		for i := 0; i < fecMaskBlockBits[block]; i++ {
			position--
			if bitAt(data[offset:offset+maskLen], position) {
				if index < 64 {
					maskLow |= 1 << uint(index)
				} else {
					maskHigh |= 1 << uint(index-64)
				}
			}
			index++
		}
	}
	return FecHeader{
		Padding:       data[0]&0x20 != 0,
		Extension:     data[0]&0x10 != 0,
		CSRCCount:     data[0] & 0x0f,
		Marker:        data[1]&0x80 != 0,
		PayloadType:   data[1] & 0x7f,
		Length:        binary.BigEndian.Uint16(data[2:4]),
		Timestamp:     binary.BigEndian.Uint32(data[4:8]),
		ProtectedSSRC: binary.BigEndian.Uint32(data[12:16]),
		SequenceBase:  binary.BigEndian.Uint16(data[16:18]),
		MaskLow:       maskLow,
		MaskHigh:      maskHigh,
	}, offset + maskLen, true
}

// bitAt reads bit position counted from the least significant bit of the
// big-endian byte string mask.
func bitAt(mask []byte, position int) bool {
	return mask[len(mask)-1-position/8]>>(uint(position)%8)&1 == 1
}

// Protected lists the sequence numbers this header protects, lowest first.
func (h FecHeader) Protected() []uint16 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/fec.rs#L203-L208
	var out []uint16
	for bit := 0; bit < 128; bit++ {
		set := h.MaskLow>>uint(bit)&1 == 1
		if bit >= 64 {
			set = h.MaskHigh>>uint(bit-64)&1 == 1
		}
		if set {
			out = append(out, h.SequenceBase+uint16(bit))
		}
	}
	return out
}

// RecoverFecPacket rebuilds the one packet of a group that did not arrive, from
// the repair payload and the other packets of its group (plaintext RTP, any
// order). ok is false unless exactly one protected packet is missing from
// received.
func RecoverFecPacket(fecPayload []byte, received [][]byte) ([]byte, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/fec.rs#L325-L381
	header, headerLen, ok := ParseFecHeader(fecPayload)
	if !ok {
		return nil, false
	}
	parity := fecPayload[headerLen:]
	protected := header.Protected()
	sequenceOf := func(packet []byte) (uint16, bool) {
		if len(packet) < fecRTPFixedLen || binary.BigEndian.Uint32(packet[8:12]) != header.ProtectedSSRC {
			return 0, false
		}
		return binary.BigEndian.Uint16(packet[2:4]), true
	}
	have := make(map[uint16]struct{}, len(received))
	for _, packet := range received {
		if sequence, ok := sequenceOf(packet); ok {
			have[sequence] = struct{}{}
		}
	}
	var lost uint16
	missing := 0
	for _, sequence := range protected {
		if _, present := have[sequence]; !present {
			lost = sequence
			missing++
		}
	}
	if missing != 1 {
		return nil, false
	}
	var head [8]byte
	head[0] = fecPayload[0] & 0x3f
	head[1] = fecPayload[1]
	copy(head[4:8], fecPayload[4:8])
	length := header.Length
	body := append([]byte(nil), parity...)
	isProtected := make(map[uint16]struct{}, len(protected))
	for _, sequence := range protected {
		isProtected[sequence] = struct{}{}
	}
	for _, packet := range received {
		sequence, ok := sequenceOf(packet)
		if !ok {
			continue
		}
		if _, member := isProtected[sequence]; !member {
			continue
		}
		for i := 0; i < len(head) && i < len(packet); i++ {
			head[i] ^= packet[i]
		}
		tail := packet[fecRTPFixedLen:]
		if len(tail) > 0xffff {
			return nil, false
		}
		length ^= uint16(len(tail))
		if len(body) < len(tail) {
			return nil, false
		}
		for i, b := range tail {
			body[i] ^= b
		}
	}
	if int(length) > len(body) {
		return nil, false
	}
	out := make([]byte, 0, fecRTPFixedLen+int(length))
	out = append(out, 0x80|(head[0]&0x3f), head[1])
	out = binary.BigEndian.AppendUint16(out, lost)
	out = append(out, head[4:8]...)
	out = binary.BigEndian.AppendUint32(out, header.ProtectedSSRC)
	out = append(out, body[:length]...)
	return out, true
}
