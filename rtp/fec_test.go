package rtp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

const fecTestSSRC uint32 = 0x12345678

func fecTestPacket(sequence uint16, body []byte) []byte {
	packet := []byte{0x80, RtpPayloadTypeH264}
	packet = binary.BigEndian.AppendUint16(packet, sequence)
	packet = binary.BigEndian.AppendUint32(packet, 9000)
	packet = binary.BigEndian.AppendUint32(packet, fecTestSSRC)
	return append(packet, body...)
}

// fecTestEncode builds the repair payload for packets of one stream with consecutive sequence
// numbers, the way a sender does: the XOR of the first eight header bytes, of the lengths past
// the fixed header and of everything after it, behind a FlexFEC-03 header.
func fecTestEncode(t *testing.T, packets [][]byte) []byte {
	t.Helper()
	return fecTestEncodeFor(t, packets, fecTestSSRC)
}

func fecTestEncodeFor(t *testing.T, packets [][]byte, ssrc uint32) []byte {
	t.Helper()
	var head [8]byte
	var length uint16
	var body []byte
	base := binary.BigEndian.Uint16(packets[0][2:4])
	var mask [2]uint64
	for _, packet := range packets {
		for i := range head {
			head[i] ^= packet[i]
		}
		tail := packet[12:]
		length ^= uint16(len(tail))
		if len(body) < len(tail) {
			body = append(body, make([]byte, len(tail)-len(body))...)
		}
		for i, b := range tail {
			body[i] ^= b
		}
		bit := int(binary.BigEndian.Uint16(packet[2:4]) - base)
		mask[bit/64] |= 1 << uint(bit%64)
	}
	out := []byte{head[0] & 0x3f, head[1]}
	out = binary.BigEndian.AppendUint16(out, length)
	out = append(out, head[4:8]...)
	out = append(out, 1, 0, 0, 0)
	out = binary.BigEndian.AppendUint32(out, ssrc)
	out = binary.BigEndian.AppendUint16(out, base)
	out = append(out, fecTestEncodeMask(mask)...)
	return append(out, body...)
}

// fecTestEncodeMask writes the mask as blocks that each lead with a k bit, 1 on the last one.
func fecTestEncodeMask(mask [2]uint64) []byte {
	highest := -1
	for bit := 0; bit < 128; bit++ {
		if mask[bit/64]>>uint(bit%64)&1 == 1 {
			highest = bit
		}
	}
	blocks := 1
	for capacity := 15; highest >= capacity; blocks++ {
		capacity += fecMaskBlockBits[blocks]
	}
	var bits []bool
	index := 0
	for block := 0; block < blocks; block++ {
		bits = append(bits, block+1 == blocks)
		for i := 0; i < fecMaskBlockBits[block]; i++ {
			bits = append(bits, mask[index/64]>>uint(index%64)&1 == 1)
			index++
		}
	}
	out := make([]byte, len(bits)/8)
	for i, set := range bits {
		if set {
			out[i/8] |= 0x80 >> uint(i%8)
		}
	}
	return out
}

func fecTestSequences(released []ReleasedPacket) []uint16 {
	var out []uint16
	for _, r := range released {
		out = append(out, binary.BigEndian.Uint16(r.Packet[2:4]))
	}
	return out
}

func equalSequences(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFecHeaderRoundTripsThroughEveryMaskLength(t *testing.T) {
	for _, span := range []int{2, 15, 16, 46, 47, 100} {
		var packets [][]byte
		for i := 0; i < span; i++ {
			packets = append(packets, fecTestPacket(65530+uint16(i), []byte{byte(i), 7, 7}))
		}
		payload := fecTestEncode(t, packets)
		header, headerLen, ok := ParseFecHeader(payload)
		if !ok {
			t.Fatalf("span %d: header did not parse", span)
		}
		if header.ProtectedSSRC != fecTestSSRC || header.SequenceBase != 65530 {
			t.Fatalf("span %d: stream %x base %d", span, header.ProtectedSSRC, header.SequenceBase)
		}
		protected := header.Protected()
		if len(protected) != span {
			t.Fatalf("span %d: protects %d packets", span, len(protected))
		}
		for i, sequence := range protected {
			if sequence != 65530+uint16(i) {
				t.Fatalf("span %d: protected[%d] = %d", span, i, sequence)
			}
		}
		if headerLen < fecFixedLen+fecStreamLen+2 {
			t.Fatalf("span %d: header length %d", span, headerLen)
		}
	}
}

func TestFecHeaderRefusesWhatItCannotRead(t *testing.T) {
	good := fecTestEncode(t, [][]byte{fecTestPacket(1, []byte{1}), fecTestPacket(2, []byte{2})})
	if _, _, ok := ParseFecHeader(good[:10]); ok {
		t.Fatal("a truncated header parsed")
	}
	retransmission := append([]byte(nil), good...)
	retransmission[0] |= 0x80
	if _, _, ok := ParseFecHeader(retransmission); ok {
		t.Fatal("a retransmission (R=1) parsed")
	}
	twoStreams := append([]byte(nil), good...)
	twoStreams[8] = 2
	if _, _, ok := ParseFecHeader(twoStreams); ok {
		t.Fatal("a repair packet for two streams parsed")
	}
	noEnd := append([]byte(nil), good...)
	noEnd[18] &^= 0x80
	if _, _, ok := ParseFecHeader(noEnd); ok {
		t.Fatal("a mask whose k bit never ends parsed")
	}
}

func TestAnyOneLostPacketIsRebuiltByteForByte(t *testing.T) {
	packets := [][]byte{
		fecTestPacket(10, []byte{1, 2, 3, 4, 5}),
		fecTestPacket(11, []byte{9}),
		fecTestPacket(12, bytes.Repeat([]byte{0xab}, 40)),
		fecTestPacket(13, []byte{0, 0, 7, 7}),
	}
	payload := fecTestEncode(t, packets)
	for lost := range packets {
		var received [][]byte
		for i, packet := range packets {
			if i != lost {
				received = append(received, packet)
			}
		}
		rebuilt, ok := RecoverFecPacket(payload, received)
		if !ok || !bytes.Equal(rebuilt, packets[lost]) {
			t.Fatalf("packet %d not rebuilt: ok=%v got %x want %x", lost, ok, rebuilt, packets[lost])
		}
	}
	if _, ok := RecoverFecPacket(payload, packets[2:]); ok {
		t.Fatal("two missing packets were rebuilt from one parity")
	}
	if _, ok := RecoverFecPacket(payload, packets); ok {
		t.Fatal("a complete group rebuilt something")
	}
}

func TestInboundFecPassesInOrderPacketsStraightThrough(t *testing.T) {
	var f InboundFec
	for sequence := uint16(10); sequence < 15; sequence++ {
		ready := f.Arrive(0, sequence, fecTestPacket(sequence, []byte{1, 2, 3}))
		if !equalSequences(fecTestSequences(ready), []uint16{sequence}) {
			t.Fatalf("sequence %d released %v", sequence, fecTestSequences(ready))
		}
	}
	if _, held := f.Deadline(); held {
		t.Fatal("nothing should be held without a gap")
	}
}

func TestInboundFecHoldsBehindAGapAndReleasesWhenALatePacketFillsIt(t *testing.T) {
	var f InboundFec
	f.Arrive(0, 10, fecTestPacket(10, []byte{1}))
	if ready := f.Arrive(1, 12, fecTestPacket(12, []byte{3})); len(ready) != 0 {
		t.Fatalf("a packet behind a gap was released: %v", fecTestSequences(ready))
	}
	if ready := f.Arrive(2, 13, fecTestPacket(13, []byte{4})); len(ready) != 0 {
		t.Fatalf("a packet behind a gap was released: %v", fecTestSequences(ready))
	}
	ready := f.Arrive(3, 11, fecTestPacket(11, []byte{2}))
	if !equalSequences(fecTestSequences(ready), []uint16{11, 12, 13}) {
		t.Fatalf("filled gap released %v", fecTestSequences(ready))
	}
	if stats := f.TakeStats(); stats.GapsSkipped != 0 {
		t.Fatalf("a filled gap counted as skipped: %+v", stats)
	}
}

func TestInboundFecRebuildsALostPacketFromItsRepair(t *testing.T) {
	packets := [][]byte{
		fecTestPacket(20, []byte{1, 1}),
		fecTestPacket(21, []byte{2, 2, 2}),
		fecTestPacket(22, []byte{3}),
		fecTestPacket(23, []byte{4, 4}),
	}
	repair := fecTestEncode(t, packets)
	var f InboundFec
	var order []uint16
	var rebuiltFlags []bool
	collect := func(released []ReleasedPacket) {
		order = append(order, fecTestSequences(released)...)
		for _, r := range released {
			rebuiltFlags = append(rebuiltFlags, r.Rebuilt)
		}
	}
	collect(f.Arrive(0, 20, packets[0]))
	collect(f.Arrive(1, 22, packets[2]))
	collect(f.Arrive(2, 23, packets[3]))
	collect(f.Repair(3, fecTestSSRC, repair))
	if !equalSequences(order, []uint16{20, 21, 22, 23}) {
		t.Fatalf("order %v", order)
	}
	if !rebuiltFlags[1] || rebuiltFlags[0] || rebuiltFlags[2] {
		t.Fatalf("rebuilt flags %v", rebuiltFlags)
	}
	stats := f.TakeStats()
	if stats.PacketsRecovered != 1 || stats.GapsSkipped != 0 || stats.RepairsReceived != 1 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestInboundFecWaitsForALatePacketWhenTwoAreMissing(t *testing.T) {
	packets := [][]byte{
		fecTestPacket(30, []byte{1}),
		fecTestPacket(31, []byte{2}),
		fecTestPacket(32, []byte{3}),
		fecTestPacket(33, []byte{4}),
	}
	repair := fecTestEncode(t, packets)
	var f InboundFec
	f.Arrive(0, 30, packets[0])
	f.Arrive(1, 33, packets[3])
	if ready := f.Repair(2, fecTestSSRC, repair); len(ready) != 0 {
		t.Fatalf("two missing packets released %v", fecTestSequences(ready))
	}
	ready := f.Arrive(3, 32, packets[2])
	if !equalSequences(fecTestSequences(ready), []uint16{31, 32, 33}) {
		t.Fatalf("late packet plus pending repair released %v", fecTestSequences(ready))
	}
	if stats := f.TakeStats(); stats.PacketsRecovered != 1 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestInboundFecGivesUpOnAGapAfterTheHold(t *testing.T) {
	var f InboundFec
	f.Arrive(0, 40, fecTestPacket(40, []byte{1}))
	f.Arrive(10, 43, fecTestPacket(43, []byte{4}))
	f.Arrive(20, 44, fecTestPacket(44, []byte{5}))
	deadline, held := f.Deadline()
	if !held || deadline != 10+FecHoldMs {
		t.Fatalf("deadline %d held %v", deadline, held)
	}
	if ready := f.Poll(deadline - 1); len(ready) != 0 {
		t.Fatalf("released early: %v", fecTestSequences(ready))
	}
	ready := f.Poll(deadline)
	if !equalSequences(fecTestSequences(ready), []uint16{43, 44}) {
		t.Fatalf("expired gap released %v", fecTestSequences(ready))
	}
	stats := f.TakeStats()
	if stats.GapsSkipped != 1 || stats.PacketsSkipped != 2 {
		t.Fatalf("stats %+v", stats)
	}
	if ready := f.Arrive(deadline+1, 42, fecTestPacket(42, []byte{3})); len(ready) != 0 {
		t.Fatalf("a packet behind what was handed on was released: %v", fecTestSequences(ready))
	}
	if stats := f.TakeStats(); stats.PacketsLate != 1 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestInboundFecWorksAcrossTheSequenceWrap(t *testing.T) {
	var f InboundFec
	f.Arrive(0, 65534, fecTestPacket(65534, []byte{1}))
	if ready := f.Arrive(1, 1, fecTestPacket(1, []byte{4})); len(ready) != 0 {
		t.Fatalf("released %v", fecTestSequences(ready))
	}
	if ready := f.Arrive(2, 0, fecTestPacket(0, []byte{3})); len(ready) != 0 {
		t.Fatalf("released %v", fecTestSequences(ready))
	}
	ready := f.Arrive(3, 65535, fecTestPacket(65535, []byte{2}))
	if !equalSequences(fecTestSequences(ready), []uint16{65535, 0, 1}) {
		t.Fatalf("wrap released %v", fecTestSequences(ready))
	}
}

func TestInboundFecTreatsAHugeJumpAsARestart(t *testing.T) {
	var f InboundFec
	f.Arrive(0, 100, fecTestPacket(100, []byte{1}))
	ready := f.Arrive(1, 5000, fecTestPacket(5000, []byte{2}))
	if !equalSequences(fecTestSequences(ready), []uint16{5000}) {
		t.Fatalf("restart released %v", fecTestSequences(ready))
	}
	if stats := f.TakeStats(); stats.GapsSkipped != 1 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestInboundFecRejectsARebuiltPacketThatIsNotTheVideoItReplaces(t *testing.T) {
	packets := [][]byte{fecTestPacket(50, []byte{1}), fecTestPacket(51, []byte{2}), fecTestPacket(52, []byte{3})}
	repair := fecTestEncode(t, packets)
	repair[len(repair)-1] ^= 0xff
	repair[1] = 0x00
	var f InboundFec
	f.Arrive(0, 50, packets[0])
	f.Arrive(1, 52, packets[2])
	if ready := f.Repair(2, fecTestSSRC, repair); len(ready) != 0 {
		t.Fatalf("an implausible rebuild was released: %v", fecTestSequences(ready))
	}
	if stats := f.TakeStats(); stats.RecoveredRejected != 1 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestInboundFecCountsMatchingParityWhenNothingWasLost(t *testing.T) {
	packets := [][]byte{fecTestPacket(60, []byte{1}), fecTestPacket(61, []byte{2}), fecTestPacket(62, []byte{3})}
	repair := fecTestEncode(t, packets)
	var f InboundFec
	for i, packet := range packets {
		f.Arrive(uint64(i), 60+uint16(i), packet)
	}
	f.Repair(10, fecTestSSRC, repair)
	if stats := f.TakeStats(); stats.ParityMatched != 1 || stats.ParityMismatched != 0 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestInboundFecIgnoresRepairForAnotherStream(t *testing.T) {
	packets := [][]byte{fecTestPacket(70, []byte{1}), fecTestPacket(71, []byte{2})}
	repair := fecTestEncode(t, packets)
	var f InboundFec
	f.Arrive(0, 70, packets[0])
	f.Repair(1, fecTestSSRC+1, repair)
	if stats := f.TakeStats(); stats.RepairsReceived != 0 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestAnIphoneMaskNamesItsBaseAsBitZero(t *testing.T) {
	header := []byte{0x00, 0xe1, 0x00, 0x00, 0x00, 0x07, 0xfc, 0x56, 1, 0, 0, 0, 0x01, 0x02, 0x03, 0x04, 0x00, 0x09, 0xff, 0x00}
	parsed, length, ok := ParseFecHeader(header)
	if !ok || length != 20 {
		t.Fatalf("ok=%v length=%d", ok, length)
	}
	if parsed.SequenceBase != 9 || parsed.Timestamp != 523350 {
		t.Fatalf("base %d timestamp %d", parsed.SequenceBase, parsed.Timestamp)
	}
	want := []uint16{9, 10, 11, 12, 13, 14, 15}
	if got := parsed.Protected(); !equalSequences(got, want) {
		t.Fatalf("protected %v want %v", got, want)
	}
}

func TestAnIphonesFirstRepairPacketIsRebuiltAsItWasSent(t *testing.T) {
	videoPacket := func(sequence uint16, timestamp uint32, marker bool, payload int) []byte {
		packet := []byte{0x90, 97}
		if marker {
			packet[1] |= 0x80
		}
		packet = binary.BigEndian.AppendUint16(packet, sequence)
		packet = binary.BigEndian.AppendUint32(packet, timestamp)
		packet = binary.BigEndian.AppendUint32(packet, 0x2da04dd6)
		packet = append(packet, 0xde, 0xbe, 0, 3, 0x30, 0x03, 0x51, 0x2e, 0x2a, 0x61, 0, 0x2b, 0x91, 0x0f, 0xf5, 0)
		for i := 0; i < payload; i++ {
			packet = append(packet, byte(i)*31+byte(sequence))
		}
		return packet
	}
	var group [][]byte
	for _, p := range []struct {
		sequence  uint16
		timestamp uint32
		marker    bool
		payload   int
	}{
		{1, 469350, false, 861}, {2, 469350, true, 860}, {3, 474570, false, 543}, {4, 474570, true, 543},
		{5, 480510, true, 979}, {6, 486900, false, 515}, {7, 486900, true, 514},
	} {
		group = append(group, videoPacket(p.sequence, p.timestamp, p.marker, p.payload))
	}
	// The sender protects the stream the packets are on; the test encoder names its own.
	payload := fecTestEncodeFor(t, group, 0x2da04dd6)
	if got := payload[:4]; !bytes.Equal(got, []byte{0x10, 0x61, 0x03, 0xe3}) {
		t.Fatalf("repair began %x, an iPhone's began 10 61 03 e3", got)
	}
	if len(payload) != 1015 {
		t.Fatalf("repair payload is %d bytes, an iPhone's was 1015", len(payload))
	}
	if !bytes.Equal(payload[18:20], []byte{0xff, 0x00}) {
		t.Fatalf("mask %x, an iPhone's was ff 00", payload[18:20])
	}
	for lost := range group {
		var received [][]byte
		for i, packet := range group {
			if i != lost {
				received = append(received, packet)
			}
		}
		rebuilt, ok := RecoverFecPacket(payload, received)
		if !ok || !bytes.Equal(rebuilt, group[lost]) {
			t.Fatalf("packet %d not rebuilt", lost+1)
		}
	}
}
