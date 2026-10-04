package mlow

import "errors"

// MLow's multiframe envelope: several complete MLow packets carried in one RTP
// payload. This is how the real client reaches a packet longer than its frame
// length: its encoder caches a fixed three blocks of 20 ms, so a frame length of
// 120 ms is two 60 ms blocks aggregated here. A decoder that does not implement
// it reads the envelope's first byte as a TOC, finds bit 7 set, calls it a SID,
// and emits comfort noise for every packet of the call.
//
// The indicator is (b & 0xC0) != 0xC0 && (b & 0x82) == 0x82. That point is
// unreachable as a real TOC: bit 7 is SID and bit 1 is FEC, and the encoder never
// emits a SID frame that carries FEC.
//
// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip/mlow/multiframe.rs#L1-L134

const (
	// multiframeMaxFrames is how many frames the real parser accepts in one envelope.
	multiframeMaxFrames = 18
	// multiframeCountMask selects the frame count in the count byte; the rest are
	// the padding flag and one bit the client ignores.
	multiframeCountMask = 0x3f
	// multiframePaddingFlag: Opus-style padding follows the count byte.
	multiframePaddingFlag = 0x40
)

var (
	ErrMultiframeTruncated     = errors.New("mlow multiframe: the envelope ended before its header did")
	ErrMultiframeBadFrameCount = errors.New("mlow multiframe: frame count is outside 1..18")
	ErrMultiframeLengthOverrun = errors.New("mlow multiframe: a declared frame length runs past the end of the envelope")
	ErrMultiframeEmptySubFrame = errors.New("mlow multiframe: a sub-frame is empty")
)

// isMultiframe reports whether payload is a multiframe envelope rather than a
// single MLow frame. Checked before the TOC is interpreted, because under the TOC
// grammar these bytes read as a SID.
func isMultiframe(payload []byte) bool {
	return len(payload) >= 2 && payload[0]&0xC0 != 0xC0 && payload[0]&0x82 == 0x82
}

// splitMultiframe splits one envelope into the complete MLow packets it carries,
// in transmission order. Each element is a whole packet with its own TOC and a
// subslice of payload. The envelope's RTP timestamp belongs to the LAST sub-frame
// and the earlier ones run backwards from it, so decoding in slice order and
// concatenating puts the audio back in time order.
func splitMultiframe(payload []byte) ([][]byte, error) {
	if len(payload) < 2 {
		return nil, ErrMultiframeTruncated
	}
	countByte := payload[1]
	frames := int(countByte & multiframeCountMask)
	if frames == 0 || frames > multiframeMaxFrames {
		return nil, ErrMultiframeBadFrameCount
	}
	rest := payload[2:]

	// Opus padding: a run of 255s, then a final byte giving the remaining padding
	// length. Only the accounting matters.
	padding := 0
	if countByte&multiframePaddingFlag != 0 {
		for {
			if len(rest) == 0 {
				return nil, ErrMultiframeTruncated
			}
			b := rest[0]
			rest = rest[1:]
			if b == 255 {
				padding += 254
				continue
			}
			padding += int(b)
			break
		}
	}

	// One explicit length per frame except the last, which takes whatever is left.
	// Same one-or-two byte encoding standard Opus uses.
	var lengths [multiframeMaxFrames]int
	declared := 0
	for i := 0; i < frames-1; i++ {
		if len(rest) == 0 {
			return nil, ErrMultiframeTruncated
		}
		length := int(rest[0])
		if rest[0] < 252 {
			rest = rest[1:]
		} else {
			if len(rest) < 2 {
				return nil, ErrMultiframeTruncated
			}
			length += int(rest[1]) * 4
			rest = rest[2:]
		}
		lengths[i] = length
		declared += length
	}

	last := len(rest) - padding - declared
	if last < 0 {
		return nil, ErrMultiframeLengthOverrun
	}
	if last == 0 {
		return nil, ErrMultiframeEmptySubFrame
	}
	lengths[frames-1] = last

	out := make([][]byte, 0, frames)
	for _, length := range lengths[:frames] {
		if length == 0 {
			return nil, ErrMultiframeEmptySubFrame
		}
		out = append(out, rest[:length])
		rest = rest[length:]
	}
	return out, nil
}
