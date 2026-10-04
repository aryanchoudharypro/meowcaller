package meowcaller

// MLow and standard Opus share RTP payload type 120, and their first bytes
// collide: 0x58 is a 120 ms MLow packet under one grammar and a 60 ms SILK
// wideband Opus packet under the other. No amount of staring at the byte
// resolves it. What resolves it is a second, independent statement by the same
// peer: the RTP timestamp step. A packet whose Opus header declares a duration
// that matches the step the peer is actually advancing by is Opus; a peer that
// is not sending Opus has no reason to make the two agree, and for the packets
// the MLow decoder accepts it is arithmetically impossible for them to.
//
// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip/opus_packet.rs#L1-L221
// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip/codec_probe.rs#L1-L108

const (
	// opusMaxFrames: RFC 6716 caps frames per packet at 48.
	opusMaxFrames = 48
	// opusMaxPacketDurationUs: a packet may not exceed 120 ms of audio.
	opusMaxPacketDurationUs = 120_000
	// codecProbeAgreeingPackets is how many consecutive agreeing packets switch
	// the verdict. One is too few: a single MLow body could coincidentally parse
	// as a valid Opus header of the right duration. Three is ~180 ms of audio.
	codecProbeAgreeingPackets = 3
)

// opusFrameDurationUs is RFC 6716 table 2: one frame's duration for a
// configuration, in microseconds (CELT 2.5 ms is not a whole millisecond).
func opusFrameDurationUs(config byte) uint32 {
	silk := [4]uint32{10_000, 20_000, 40_000, 60_000}
	celt := [4]uint32{2_500, 5_000, 10_000, 20_000}
	switch {
	case config < 12:
		return silk[config&3]
	case config < 16:
		// Hybrid carries only 10 and 20 ms, selected by the low bit.
		if config&1 == 0 {
			return 10_000
		}
		return 20_000
	default:
		return celt[config&3]
	}
}

// opusPacketSamples is the samples per channel a structurally valid standard
// Opus packet (RFC 6716 section 3.1) declares at clockRate. ok is false for
// anything that is not one: a truncated code-2 length, an odd body under code 1,
// a code-3 frame count of zero or past 48, padding that overruns, VBR lengths
// that do not fit, or a duration past 120 ms. Being strict is the point: a
// permissive reader would accept MLow bodies as "valid Opus".
func opusPacketSamples(payload []byte, clockRate uint32) (samples uint32, ok bool) {
	if len(payload) == 0 {
		return 0, false
	}
	toc, rest := payload[0], payload[1:]
	frameUs := opusFrameDurationUs(toc >> 3)
	// opusLength reads the one-or-two byte frame length encoding.
	opusLength := func(b []byte) (length, width int, ok bool) {
		if len(b) == 0 {
			return 0, 0, false
		}
		if b[0] < 252 {
			return int(b[0]), 1, true
		}
		if len(b) < 2 {
			return 0, 0, false
		}
		return int(b[0]) + int(b[1])*4, 2, true
	}

	var frames uint32
	switch toc & 0x03 {
	case 0:
		frames = 1
	case 1:
		// Two equal frames, so the body has to split evenly.
		if len(rest)%2 != 0 {
			return 0, false
		}
		frames = 2
	case 2:
		// Two frames with an explicit length for the first.
		length, width, valid := opusLength(rest)
		if !valid || length > len(rest)-width {
			return 0, false
		}
		frames = 2
	default:
		// An explicit frame count, optionally VBR, optionally padded.
		if len(rest) == 0 {
			return 0, false
		}
		countByte, remaining := rest[0], rest[1:]
		frames = uint32(countByte & 0x3f)
		if frames == 0 || frames > opusMaxFrames {
			return 0, false
		}
		// Padding is a run of 255s ended by a final byte, and the total accumulates.
		padding := 0
		if countByte&0x40 != 0 {
			for {
				if len(remaining) == 0 {
					return 0, false
				}
				pad := remaining[0]
				remaining = remaining[1:]
				if pad == 255 {
					padding += 254
					continue
				}
				padding += int(pad)
				break
			}
		}
		bodies := len(remaining) - padding
		if bodies < 0 {
			return 0, false
		}
		if countByte&0x80 == 0 {
			// CBR: every frame is the same size, so the bytes divide evenly.
			if bodies%int(frames) != 0 {
				return 0, false
			}
		} else {
			// VBR carries M-1 explicit lengths and leaves the last frame implicit.
			lengths := remaining[:bodies]
			declared, header := 0, 0
			for i := uint32(1); i < frames; i++ {
				length, width, valid := opusLength(lengths)
				if !valid {
					return 0, false
				}
				lengths = lengths[width:]
				declared += length
				header += width
			}
			if header+declared > bodies {
				return 0, false
			}
		}
	}

	totalUs := uint64(frameUs) * uint64(frames)
	if totalUs > opusMaxPacketDurationUs {
		return 0, false
	}
	// An RTP clock that cannot express the declared duration means the two are
	// not describing the same stream: a rejection, not a rounding problem.
	numerator := totalUs * uint64(clockRate)
	if numerator%1_000_000 != 0 {
		return 0, false
	}
	return uint32(numerator / 1_000_000), true
}

// inboundCodecProbe watches one inbound stream for evidence that the peer is
// sending standard Opus on the payload type MLow shares. It never reads the
// payload alone: it asks whether the duration the Opus header declares and the
// step the RTP timestamps advance by agree.
type inboundCodecProbe struct {
	havePrevious bool
	sequence     uint16
	timestamp    uint32
	// agreeing is consecutive packets whose declared Opus duration matched the
	// observed timestamp step.
	agreeing uint8
	// opus is promote-once: a peer does not change codec mid-call on its own,
	// and letting a second opinion thrash the decoder is worse than staying
	// wrong in one direction.
	opus bool
}

// observe feeds one authenticated inbound payload. agrees reports that this
// packet's Opus header and its timestamp step tell the same story, which no
// MLow packet at that step can; settled reports that it is the packet that
// settled the stream as standard Opus.
func (p *inboundCodecProbe) observe(payload []byte, sequence uint16, timestamp uint32) (agrees, settled bool) {
	consecutive := p.havePrevious && sequence == p.sequence+1
	span := timestamp - p.timestamp
	p.havePrevious, p.sequence, p.timestamp = true, sequence, timestamp
	if p.opus || !consecutive {
		// Without the step there is no second statement to cross-check against,
		// so abstain rather than fall back to reading the payload alone.
		return false, false
	}
	// The peer must also be pacing at the cadence the call negotiated. Without
	// this the agreement is not evidence at all: MLow reads TOC bits 4:3 as
	// {10,20,60,120} ms and an Opus SILK TOC reads the SAME bits as
	// {10,20,40,60}, so the two grammars agree by construction at 10 and 20 ms.
	// At the negotiated 960-sample step the collision cannot happen: those same
	// MLow durations read as 40 ms of Opus, and 960 is not a multiple of 640.
	if span != FrameSamples {
		p.agreeing = 0
		return false, false
	}
	if declared, ok := opusPacketSamples(payload, SampleRate); !ok || declared != span {
		p.agreeing = 0
		return false, false
	}
	if p.agreeing++; p.agreeing < codecProbeAgreeingPackets {
		return true, false
	}
	p.opus = true
	return true, true
}
