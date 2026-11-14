package rtp

import (
	"encoding/binary"
	"strconv"
)

// RtcpFeedback is the transport feedback found in one compound RTCP packet.
type RtcpFeedback struct {
	// Nacked maps a media SSRC to the sequence numbers a generic NACK
	// (RFC 4585, PT 205 FMT 1) asked for again.
	Nacked map[uint32][]uint16
	// RembBps is the receiver-estimated maximum bitrate (PT 206 FMT 15,
	// "REMB"), 0 when none was present; RembSsrcs are the streams it covers.
	RembBps   uint64
	RembSsrcs []uint32
	// Types lists every packet type/format in the compound packet, for logs.
	Types []string
}

// ParseRtcpFeedback walks a decrypted compound RTCP packet for NACKs and REMB.
func ParseRtcpFeedback(data []byte) RtcpFeedback {
	var fb RtcpFeedback
	for len(data) >= 4 {
		if data[0]>>6 != 2 {
			break
		}
		format := data[0] & 0x1f
		pt := data[1]
		words := int(binary.BigEndian.Uint16(data[2:4]))
		size := (words + 1) * 4
		if size > len(data) {
			size = len(data)
		}
		pkt := data[:size]
		data = data[size:]
		fb.Types = append(fb.Types, rtcpTypeName(pt, format))
		if len(pkt) < 12 {
			continue
		}
		mediaSSRC := binary.BigEndian.Uint32(pkt[8:12])
		switch {
		case pt == 205 && format == 1:
			for fci := pkt[12:]; len(fci) >= 4; fci = fci[4:] {
				pid := binary.BigEndian.Uint16(fci[0:2])
				blp := binary.BigEndian.Uint16(fci[2:4])
				if fb.Nacked == nil {
					fb.Nacked = make(map[uint32][]uint16)
				}
				fb.Nacked[mediaSSRC] = append(fb.Nacked[mediaSSRC], pid)
				for bit := uint16(0); bit < 16; bit++ {
					if blp&(1<<bit) != 0 {
						fb.Nacked[mediaSSRC] = append(fb.Nacked[mediaSSRC], pid+bit+1)
					}
				}
			}
		case pt == 206 && format == 15 && len(pkt) >= 20 && string(pkt[12:16]) == "REMB":
			count := int(pkt[16])
			exp := uint(pkt[17] >> 2)
			mantissa := uint64(pkt[17]&0x03)<<16 | uint64(pkt[18])<<8 | uint64(pkt[19])
			fb.RembBps = mantissa << exp
			for i, ssrcs := 0, pkt[20:]; i < count && len(ssrcs) >= 4; i, ssrcs = i+1, ssrcs[4:] {
				fb.RembSsrcs = append(fb.RembSsrcs, binary.BigEndian.Uint32(ssrcs[:4]))
			}
		}
	}
	return fb
}

func rtcpTypeName(pt, format uint8) string {
	switch pt {
	case 200:
		return "SR"
	case 201:
		return "RR"
	case 202:
		return "SDES"
	case 203:
		return "BYE"
	case 204:
		return "APP"
	case 205:
		switch format {
		case 1:
			return "NACK"
		case 15:
			return "TWCC"
		}
		return "RTPFB"
	case 206:
		switch format {
		case 1:
			return "PLI"
		case 4:
			return "FIR"
		case 15:
			return "AFB"
		}
		return "PSFB"
	}
	return "PT" + strconv.Itoa(int(pt))
}
