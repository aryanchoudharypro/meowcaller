package meowcaller

// rtpClockVotes is how many packets in a row must show the same clock before
// a peer's audio timestamps are rescaled.
const rtpClockVotes = 5

// rtpClockScaler turns a peer's audio RTP timestamps into 16 kHz ticks when
// its clock runs faster: 48 kHz (WA's enable_48khz_rtp_clock) or the 24 kHz a
// super-wideband stream might count in. Everything downstream (the codec
// probe, the playout, an OpusPacketSink) counts 16 kHz ticks. The clock is
// read off the stream itself: the step between two packets with consecutive
// sequence numbers against the first one's Opus duration.
type rtpClockScaler struct {
	// num/den is ticks per 16 kHz sample: 1/1 until a faster clock settles,
	// then 3/1 (48 kHz) or 3/2 (24 kHz).
	num, den uint64
	// candidate is the clock the last packets voted for, votes how many.
	candNum, candDen uint64
	votes            int
	// The previous packet, for the next vote.
	have     bool
	lastSeq  uint16
	lastTS   uint32
	lastDur  uint32 // its Opus duration in 16 kHz samples, 0 if unknown
	unwrap   uint64 // running timestamp in the peer's ticks, without wraps
	unwrapTS uint32
	// The output is base16 + (unwrap - baseRaw) * den / num.
	baseRaw, base16 uint64
}

// normalize returns ts in 16 kHz ticks. enabled says whether the call could
// run a faster clock at all (super-wideband negotiated or the server's 48 kHz
// clock on); without it nothing is measured, so an MLow stream, whose header
// reads as unrelated Opus durations, can never trip a rescale. The output
// stays continuous across the moment a faster clock settles.
func (s *rtpClockScaler) normalize(payload []byte, seq uint16, ts uint32, enabled bool) uint32 {
	if s.num == 0 {
		s.num, s.den = 1, 1
	}
	if !s.have {
		s.unwrap, s.unwrapTS = uint64(ts), ts
		s.baseRaw, s.base16 = s.unwrap, s.unwrap
	}
	if enabled && s.num == 1 {
		num, den := s.vote(payload, seq, ts)
		if num != 1 {
			s.base16 = s.at(s.unwrap)
			s.baseRaw = s.unwrap
			s.num, s.den = num, den
		}
	}
	s.have = true
	if delta := int32(ts - s.unwrapTS); delta > 0 {
		s.unwrap += uint64(delta)
		s.unwrapTS = ts
		return uint32(s.at(s.unwrap))
	}
	// A late or repeated packet: place it behind the running count.
	back := uint64(-int64(int32(ts - s.unwrapTS)))
	if back > s.unwrap-s.baseRaw {
		return uint32(s.at(s.unwrap)) - uint32(back)
	}
	return uint32(s.at(s.unwrap - back))
}

// at maps a running peer timestamp to 16 kHz ticks.
func (s *rtpClockScaler) at(raw uint64) uint64 {
	return s.base16 + (raw-s.baseRaw)*s.den/s.num
}

// vote compares this packet's timestamp step to the previous packet's
// duration and settles a faster clock once rtpClockVotes agree.
func (s *rtpClockScaler) vote(payload []byte, seq uint16, ts uint32) (num, den uint64) {
	num, den = 1, 1
	dur, ok := opusPacketSamples(payload, SampleRate)
	if !ok {
		dur = 0
	}
	if s.have && seq == s.lastSeq+1 && s.lastDur > 0 {
		step := uint64(ts - s.lastTS)
		d := uint64(s.lastDur)
		var cn, cd uint64
		switch {
		case step == d:
			cn, cd = 1, 1
		case step == 3*d:
			cn, cd = 3, 1
		case 2*step == 3*d:
			cn, cd = 3, 2
		}
		switch {
		case cn == 0:
			s.votes = 0
		case cn == s.candNum && cd == s.candDen:
			s.votes++
		default:
			s.candNum, s.candDen, s.votes = cn, cd, 1
		}
		if s.votes >= rtpClockVotes && s.candNum != 1 {
			num, den = s.candNum, s.candDen
		}
	}
	s.lastSeq, s.lastTS, s.lastDur = seq, ts, dur
	return num, den
}

// clockRate is the peer's audio RTP clock as settled so far.
func (s *rtpClockScaler) clockRate() int {
	if s.num == 0 {
		return SampleRate
	}
	return int(uint64(SampleRate) * s.num / s.den)
}
