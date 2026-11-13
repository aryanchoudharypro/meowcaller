package rtp

import "encoding/binary"

const (
	// FecHoldMs is the longest wait behind a gap, counted from each packet's arrival. It is a
	// local latency policy: it lets reordered originals fill a gap before any repair is seen.
	FecHoldMs uint64 = 100
	// fecHoldMax bounds the packets held behind gaps so a burst cannot grow the hold without bound.
	fecHoldMax = 128
	// fecRecentMax is how many recent packets are kept for rebuilding, well past the widest span
	// a repair packet's mask names.
	fecRecentMax = 256
	// fecPendingMax is how many repair packets wait for a late packet while two or more of their
	// group are still missing.
	fecPendingMax = 32
	// fecRestartJump is a forward jump this far: a restarted stream, not a gap worth waiting on.
	fecRestartJump = 1000
)

// InboundFecStats is what inbound FEC did since the last TakeStats. Counts only.
type InboundFecStats struct {
	// RepairsReceived is repair packets received for the stream being reassembled.
	RepairsReceived uint32
	// PacketsRecovered is lost packets rebuilt from a repair packet and handed on.
	PacketsRecovered uint32
	// ParityMatched is repair packets that arrived with their whole group present and whose
	// parity matched it.
	ParityMatched uint32
	// ParityMismatched is the same, with parity that did not match.
	ParityMismatched uint32
	// RepairsInsufficient is repair packets given up on with two or more of their group still
	// missing, which one parity cannot rebuild.
	RepairsInsufficient uint32
	// RepairsLate is repair packets whose one missing packet was no longer wanted.
	RepairsLate uint32
	// RecoveredRejected is rebuilt packets that did not look like the video packet they replace.
	RecoveredRejected uint32
	// GapsSkipped is gaps given up on: the hold ran out with the packet still missing, or the
	// stream jumped past it.
	GapsSkipped uint32
	// PacketsSkipped is packets in those gaps.
	PacketsSkipped uint32
	// PacketsLate is packets that arrived behind what was already handed on and were dropped.
	PacketsLate uint32
}

// Add folds other into s.
func (s *InboundFecStats) Add(other InboundFecStats) {
	s.RepairsReceived += other.RepairsReceived
	s.PacketsRecovered += other.PacketsRecovered
	s.ParityMatched += other.ParityMatched
	s.ParityMismatched += other.ParityMismatched
	s.RepairsInsufficient += other.RepairsInsufficient
	s.RepairsLate += other.RepairsLate
	s.RecoveredRejected += other.RecoveredRejected
	s.GapsSkipped += other.GapsSkipped
	s.PacketsSkipped += other.PacketsSkipped
	s.PacketsLate += other.PacketsLate
}

// ReleasedPacket is a packet handed on to reassembly.
type ReleasedPacket struct {
	// Packet is plaintext: the header as it arrived, then the whole decrypted payload.
	Packet []byte
	// Rebuilt is set for a packet rebuilt from a repair packet rather than received. Its
	// piggybacked audio was never delivered, unlike a received packet's.
	Rebuilt bool
}

type heldPacket struct {
	sequence uint16
	packet   []byte
	rebuilt  bool
	since    uint64
}

type recentPacket struct {
	sequence uint16
	packet   []byte
}

type pendingRepair struct {
	since   uint64
	payload []byte
}

// InboundFec holds inbound video packets behind a sequence gap and rebuilds lost ones from
// repair packets. The zero value is ready to use. Times are milliseconds on any monotonic clock.
type InboundFec struct {
	expected    uint16
	hasExpected bool
	held        []heldPacket
	recent      []recentPacket
	pending     []pendingRepair
	stats       InboundFecStats
}

type fecAttempt int

const (
	fecAttemptDone fecAttempt = iota
	fecAttemptShort
)

// distance is the signed distance from from to to, across the sequence number wrap.
func fecDistance(from, to uint16) int16 {
	return int16(to - from)
}

// Reset forgets the stream: the next packet starts a new one. Counters are kept.
func (f *InboundFec) Reset() {
	stats := f.stats
	*f = InboundFec{stats: stats}
}

// TakeStats returns the counters and clears them.
func (f *InboundFec) TakeStats() InboundFecStats {
	stats := f.stats
	f.stats = InboundFecStats{}
	return stats
}

// Deadline is when the oldest held packet's hold runs out; ok is false when none is held.
func (f *InboundFec) Deadline() (uint64, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/fec_receive.rs#L131-L138
	if len(f.held) == 0 {
		return 0, false
	}
	oldest := f.held[0].since
	for _, held := range f.held[1:] {
		if held.since < oldest {
			oldest = held.since
		}
	}
	return oldest + FecHoldMs, true
}

// Poll gives up on gaps whose hold ran out though nothing arrived since, and returns the
// packets reassembly may take now, in order.
func (f *InboundFec) Poll(now uint64) []ReleasedPacket {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/fec_receive.rs#L142-L146
	var ready []ReleasedPacket
	f.expire(now, &ready)
	return ready
}

// Arrive takes one video packet (plaintext) and returns the packets reassembly may take now,
// in order.
func (f *InboundFec) Arrive(now uint64, sequence uint16, packet []byte) []ReleasedPacket {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/fec_receive.rs#L149-L155
	var ready []ReleasedPacket
	f.place(now, sequence, packet, false, &ready)
	f.retryPending(now, &ready)
	f.expire(now, &ready)
	return ready
}

// Repair takes one repair packet payload, already decrypted, for the stream ssrc, and returns
// the packets reassembly may take now, the rebuilt one among them.
func (f *InboundFec) Repair(now uint64, ssrc uint32, payload []byte) []ReleasedPacket {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/fec_receive.rs#L159-L177
	var ready []ReleasedPacket
	if header, _, ok := ParseFecHeader(payload); ok && header.ProtectedSSRC == ssrc {
		f.stats.RepairsReceived++
		if f.attempt(now, payload, true, &ready) == fecAttemptShort {
			if len(f.pending) == fecPendingMax {
				f.pending = f.pending[1:]
				f.stats.RepairsInsufficient++
			}
			f.pending = append(f.pending, pendingRepair{since: now, payload: append([]byte(nil), payload...)})
		}
		f.retryPending(now, &ready)
	}
	f.expire(now, &ready)
	return ready
}

// place hands a received or rebuilt packet on if it is next, holds it behind a gap, or drops
// it if reassembly is already past it.
func (f *InboundFec) place(now uint64, sequence uint16, packet []byte, rebuilt bool, ready *[]ReleasedPacket) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/fec_receive.rs#L181-L239
	f.remember(sequence, packet)
	if !f.hasExpected {
		f.expected = sequence + 1
		f.hasExpected = true
		*ready = append(*ready, ReleasedPacket{Packet: packet, Rebuilt: rebuilt})
		return
	}
	ahead := fecDistance(f.expected, sequence)
	switch {
	case ahead == 0:
		*ready = append(*ready, ReleasedPacket{Packet: packet, Rebuilt: rebuilt})
		f.expected = sequence + 1
		f.drain(ready)
	case ahead < 0:
		f.stats.PacketsLate++
	case ahead >= fecRestartJump:
		f.stats.GapsSkipped++
		skipped := uint32(ahead)
		if held := uint32(len(f.held)); skipped > held {
			skipped -= held
		} else {
			skipped = 0
		}
		f.stats.PacketsSkipped += skipped
		for _, held := range f.held {
			*ready = append(*ready, ReleasedPacket{Packet: held.packet, Rebuilt: held.rebuilt})
		}
		f.held = nil
		*ready = append(*ready, ReleasedPacket{Packet: packet, Rebuilt: rebuilt})
		f.expected = sequence + 1
	default:
		at := 0
		for at < len(f.held) && fecDistance(f.expected, f.held[at].sequence) < ahead {
			at++
		}
		if at < len(f.held) && f.held[at].sequence == sequence {
			f.stats.PacketsLate++
			return
		}
		f.held = append(f.held, heldPacket{})
		copy(f.held[at+1:], f.held[at:])
		f.held[at] = heldPacket{sequence: sequence, packet: packet, rebuilt: rebuilt, since: now}
	}
}

// attempt rebuilds from a repair packet if exactly one of its group is missing. first marks its
// arrival; later attempts are retries of one already counted.
func (f *InboundFec) attempt(now uint64, payload []byte, first bool, ready *[]ReleasedPacket) fecAttempt {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/fec_receive.rs#L243-L316
	header, _, ok := ParseFecHeader(payload)
	if !ok {
		return fecAttemptDone
	}
	var group [][]byte
	var missing []uint16
	for _, sequence := range header.Protected() {
		if packet, found := f.recentPacket(sequence); found {
			group = append(group, packet)
		} else {
			missing = append(missing, sequence)
		}
	}
	wanted := func(sequence uint16) bool {
		return f.hasExpected && fecDistance(f.expected, sequence) >= 0
	}
	switch {
	case len(missing) == 0:
		if first {
			matched := false
			if len(group) > 0 {
				if rebuilt, ok := RecoverFecPacket(payload, group[1:]); ok {
					matched = string(rebuilt) == string(group[0])
				}
			}
			if matched {
				f.stats.ParityMatched++
			} else {
				f.stats.ParityMismatched++
			}
		}
		return fecAttemptDone
	case len(missing) == 1:
		lost := missing[0]
		if !wanted(lost) {
			f.stats.RepairsLate++
			return fecAttemptDone
		}
		rebuilt, ok := RecoverFecPacket(payload, group)
		if ok && fecPlausible(rebuilt, lost, header.ProtectedSSRC) {
			f.stats.PacketsRecovered++
			f.place(now, lost, rebuilt, true, ready)
		} else {
			f.stats.RecoveredRejected++
		}
		return fecAttemptDone
	}
	for _, sequence := range missing {
		if wanted(sequence) {
			return fecAttemptShort
		}
	}
	f.stats.RepairsLate++
	return fecAttemptDone
}

// retryPending tries the waiting repair packets again, until none rebuilds anything more.
func (f *InboundFec) retryPending(now uint64, ready *[]ReleasedPacket) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/fec_receive.rs#L319-L337
	for {
		recovered := f.stats.PacketsRecovered
		index := 0
		for index < len(f.pending) {
			payload := f.pending[index].payload
			f.pending[index].payload = nil
			if f.attempt(now, payload, false, ready) == fecAttemptShort {
				f.pending[index].payload = payload
				index++
				continue
			}
			f.pending = append(f.pending[:index], f.pending[index+1:]...)
		}
		if f.stats.PacketsRecovered == recovered {
			break
		}
	}
}

func (f *InboundFec) recentPacket(sequence uint16) ([]byte, bool) {
	for _, kept := range f.recent {
		if kept.sequence == sequence {
			return kept.packet, true
		}
	}
	return nil, false
}

func (f *InboundFec) remember(sequence uint16, packet []byte) {
	if _, found := f.recentPacket(sequence); found {
		return
	}
	if len(f.recent) == fecRecentMax {
		f.recent = f.recent[1:]
	}
	f.recent = append(f.recent, recentPacket{sequence: sequence, packet: packet})
}

// drain hands on the held packets that now follow without a gap.
func (f *InboundFec) drain(ready *[]ReleasedPacket) {
	for f.hasExpected && len(f.held) > 0 && f.held[0].sequence == f.expected {
		held := f.held[0]
		f.held = f.held[1:]
		*ready = append(*ready, ReleasedPacket{Packet: held.packet, Rebuilt: held.rebuilt})
		f.expected++
	}
}

// expire gives up on gaps once the oldest packet behind them has waited its hold, or too much
// is held; and on repair packets that waited as long. Each packet waits from its own arrival,
// so several gaps in a row do not add their holds up.
func (f *InboundFec) expire(now uint64, ready *[]ReleasedPacket) {
	for f.hasExpected && len(f.held) > 0 {
		deadline, _ := f.Deadline()
		if now < deadline && len(f.held) <= fecHoldMax {
			break
		}
		first := f.held[0].sequence
		if skipped := fecDistance(f.expected, first); skipped > 0 {
			f.stats.PacketsSkipped += uint32(skipped)
		}
		f.stats.GapsSkipped++
		f.expected = first
		f.drain(ready)
	}
	for len(f.pending) > 0 && now-minUint64(now, f.pending[0].since) >= FecHoldMs {
		f.pending = f.pending[1:]
		f.stats.RepairsInsufficient++
	}
}

func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// fecPlausible reports whether a rebuilt packet is the video packet it replaces: the right
// stream and sequence number, the H.264 payload type, and a header that leaves a payload.
func fecPlausible(packet []byte, sequence uint16, ssrc uint32) bool {
	if len(packet) <= fecRTPFixedLen || packet[0]>>6 != rtpVersion || packet[1]&0x7f != RtpPayloadTypeH264 {
		return false
	}
	if binary.BigEndian.Uint16(packet[2:4]) != sequence || binary.BigEndian.Uint32(packet[8:12]) != ssrc {
		return false
	}
	headerLen, ok := RtpHeaderByteLength(packet)
	return ok && headerLen < len(packet)
}
