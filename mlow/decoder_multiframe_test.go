package mlow

import (
	"encoding/binary"
	"encoding/hex"
	"math"
	"os"
	"testing"
)

// Ports of the multi-frame, envelope and endpoint tests in
// https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip/mlow/decoder.rs#L631-L1098
// and multiframe.rs. The 120 ms fixtures are theirs (testdata/PROVENANCE.md there).

func loadHexFrames(t *testing.T, name string) [][]byte {
	t.Helper()
	var frames []string
	loadJSON(t, name, &frames)
	out := make([][]byte, len(frames))
	for i, hf := range frames {
		fb, err := hex.DecodeString(hf)
		if err != nil {
			t.Fatalf("%s frame %d: bad hex: %v", name, i, err)
		}
		out[i] = fb
	}
	return out
}

func loadPCM16(t *testing.T, name string) []float32 {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	out := make([]float32, len(raw)/2)
	for i := range out {
		out[i] = float32(int16(binary.LittleEndian.Uint16(raw[2*i:]))) / 32768.0
	}
	return out
}

func correlation(a, b []float32) float64 {
	n := float64(len(a))
	var ma, mb float64
	for i := range a {
		ma += float64(a[i])
		mb += float64(b[i])
	}
	ma, mb = ma/n, mb/n
	var sxy, sxx, syy float64
	for i := range a {
		da, db := float64(a[i])-ma, float64(b[i])-mb
		sxy += da * db
		sxx += da * da
		syy += db * db
	}
	return sxy / math.Sqrt(sxx*syy)
}

func allZero(pcm []float32) bool {
	for _, s := range pcm {
		if s != 0 {
			return false
		}
	}
	return true
}

func TestInternalFrameCountMatchesTheReferenceGeometry(t *testing.T) {
	for ms, want := range map[int]int{20: 1, 60: 3, 120: 6, 10: 0} {
		if got := internalFrames(ms); got != want {
			t.Errorf("internalFrames(%d) = %d, want %d", ms, got, want)
		}
	}
}

func TestPacketsDecodeToTheirDeclaredDuration(t *testing.T) {
	// A 120 ms packet (0x58) is six internal frames, a 20 ms one (0x48) is one.
	if got := len(NewMlowDecoder().Decode([]byte{0x58, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22})); got != 6*SmplIntfLen {
		t.Fatalf("120 ms packet decoded to %d samples, want %d", got, 6*SmplIntfLen)
	}
	if got := len(NewMlowDecoder().Decode([]byte{0x48, 0xAA, 0xBB, 0xCC})); got != SmplIntfLen {
		t.Fatalf("20 ms packet decoded to %d samples, want %d", got, SmplIntfLen)
	}
	// 10 ms runs a geometry the synthesis does not implement: dropped, but still
	// occupying the 10 ms it declares.
	dec := NewMlowDecoder()
	out := dec.Decode([]byte{0x40, 0xAA, 0xBB, 0xCC})
	if len(out) != 160 || !allZero(out) {
		t.Fatalf("10 ms active packet = %d samples (silent %v), want 160 of silence", len(out), allZero(out))
	}
	if report := dec.TakeFrameReport(); report.OffPoint != 1 || report.Decoded != 0 {
		t.Fatalf("10 ms active packet report = %+v, want one off-point frame", report)
	}
	// A SID that declares 120 ms (0x98) fills 120 ms of silence and is benign.
	sid := dec.Decode([]byte{0x98, 0xAA, 0xBB, 0xCC})
	if len(sid) != 6*SmplIntfLen || !allZero(sid) {
		t.Fatalf("120 ms SID = %d samples", len(sid))
	}
	if report := dec.TakeFrameReport(); report.InactiveOrSID != 1 || report.Concealed != 0 || report.OffPoint != 0 {
		t.Fatalf("SID report = %+v, want one inactive frame", report)
	}
}

// The content check: real 120 ms packets against the reference decoder's own
// output for the same bytes. Geometry alone is not enough, since running the
// loop the wrong number of times still produces plausibly-shaped audio.
func TestMultiFrameDecodeMatchesTheReference(t *testing.T) {
	for _, tc := range []struct {
		frames, ref string
		minCorr     float64
	}{
		{"mlow_120ms_frames.json", "ref_120ms_expected.raw", 0.999},
		// Both halves from the shipped engine; the decoders agree closely
		// without sharing lineage.
		{"wasm_derived_120ms_frames.json", "wasm_derived_120ms_ref.raw", 0.95},
	} {
		frames := loadHexFrames(t, tc.frames)
		ref := loadPCM16(t, tc.ref)
		if len(frames) != 8 || len(ref) != len(frames)*6*SmplIntfLen {
			t.Fatalf("%s: %d frames against %d reference samples", tc.frames, len(frames), len(ref))
		}
		dec := NewMlowDecoder()
		var out []float32
		var report FrameReport
		for i, frame := range frames {
			if frame[0] != 0x58 {
				t.Fatalf("%s frame %d is TOC %#02x, not a 120 ms packet", tc.frames, i, frame[0])
			}
			pcm := dec.Decode(frame)
			if allZero(pcm) {
				t.Fatalf("%s frame %d: a valid packet was rejected as malformed", tc.frames, i)
			}
			out = append(out, pcm...)
			r := dec.TakeFrameReport()
			report.Decoded += r.Decoded
			report.Concealed += r.Concealed
		}
		if len(out) != len(ref) {
			t.Fatalf("%s: decoded %d samples, want %d", tc.frames, len(out), len(ref))
		}
		if report.Decoded != uint32(len(frames)*6) || report.Concealed != 0 {
			t.Fatalf("%s: report = %+v, want every internal frame decoded", tc.frames, report)
		}
		corr := correlation(ref, out)
		t.Logf("%s: lag-0 correlation %.6f", tc.frames, corr)
		if corr <= tc.minCorr {
			t.Fatalf("%s: lag-0 correlation %.6f, want > %.3f", tc.frames, corr, tc.minCorr)
		}
	}
}

func TestEndpointWindowMatchesTheShippedDecoder(t *testing.T) {
	for _, tc := range []struct {
		consumed uint32
		valid    bool
	}{{40, true}, {42, true}, {44, true}, {45, false}, {39, false}} {
		if got := endpointIsValid(40, tc.consumed); got != tc.valid {
			t.Errorf("endpointIsValid(40, %d) = %v, want %v", tc.consumed, got, tc.valid)
		}
	}
}

// A body whose decode consumes far more bits than it holds is malformed: the
// range decoder returns zero past the end without flagging it, so the synthesis
// runs on invented symbols and can diverge to full scale. It must be concealed,
// and leave no trace in the next packet.
func TestAConcealedFrameDoesNotContaminateTheNext(t *testing.T) {
	real := loadHexFrames(t, "inbound_capture_frames.json")[0]
	want := NewMlowDecoder().Decode(real)

	contaminated := NewMlowDecoder()
	bad := contaminated.Decode([]byte{0x58, 0x03, 0x1a, 0xfb, 0x0a})
	if len(bad) != 6*SmplIntfLen || !allZero(bad) {
		t.Fatalf("an over-running body must be concealed at its full 120 ms slot, got %d samples", len(bad))
	}
	if report := contaminated.TakeFrameReport(); report.Concealed != 1 || report.Decoded != 0 {
		t.Fatalf("over-running body report = %+v, want one concealed frame", report)
	}
	got := contaminated.Decode(real)
	if len(got) != len(want) {
		t.Fatalf("decoded %d samples after a concealed frame, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d differs after a concealed frame: state from the malformed body leaked", i)
		}
	}
}

func multiframeEnvelope(frames ...[]byte) []byte {
	out := []byte{0x82 | (frames[0][0] & 0x39), byte(len(frames))}
	for _, frame := range frames[:len(frames)-1] {
		out = append(out, byte(len(frame)))
	}
	for _, frame := range frames {
		out = append(out, frame...)
	}
	return out
}

func TestSplitMultiframe(t *testing.T) {
	first := append([]byte{0x50}, make([]byte, 40)...)
	second := append([]byte{0x50}, make([]byte, 50)...)
	for i := range first[1:] {
		first[1+i] = byte(i)
	}
	for i := range second[1:] {
		second[1+i] = byte(40 + i)
	}
	packet := multiframeEnvelope(first, second)
	if !isMultiframe(packet) {
		t.Fatal("a two-frame envelope was not recognised")
	}
	frames, err := splitMultiframe(packet)
	if err != nil || len(frames) != 2 || string(frames[0]) != string(first) || string(frames[1]) != string(second) {
		t.Fatalf("split = %d frames, err %v", len(frames), err)
	}

	// Opus-style padding after the count byte is skipped, not decoded.
	padded := []byte{packet[0], 0x02 | multiframePaddingFlag, 3, byte(len(first))}
	padded = append(padded, first...)
	padded = append(padded, second...)
	padded = append(padded, 0, 0, 0)
	if frames, err = splitMultiframe(padded); err != nil || len(frames) != 2 || string(frames[1]) != string(second) {
		t.Fatalf("padded split = %d frames, err %v", len(frames), err)
	}

	// The two-byte length form: first + second*4.
	long := append([]byte{0x50}, make([]byte, 299)...)
	twoByte := []byte{packet[0], 0x02, 252, 12}
	twoByte = append(twoByte, long...)
	twoByte = append(twoByte, second...)
	if frames, err = splitMultiframe(twoByte); err != nil || len(frames[0]) != 300 || string(frames[1]) != string(second) {
		t.Fatalf("two-byte length split: err %v", err)
	}

	for name, bad := range map[string][]byte{
		"zero frames":            {0x92, 0x00, 0x50},
		"too many frames":        {0x92, 19, 0x50},
		"length past the end":    {0x92, 0x02, 200, 0x50, 0x01},
		"first frame takes all":  append([]byte{0x92, 0x02, 10}, make([]byte, 10)...),
		"empty declared frame":   {0x92, 0x02, 0, 0x50, 0x01},
		"padding past the end":   {0x92, 0x01 | multiframePaddingFlag, 9, 0x50},
		"truncated length bytes": {0x92, 0x03, 1},
	} {
		if _, err = splitMultiframe(bad); err == nil {
			t.Errorf("%s: split accepted a malformed envelope", name)
		}
	}
}

// The indicator must be unreachable as a frame the decoder would otherwise
// decode: every byte the splitter claims reads as a SID under the TOC grammar.
func TestMultiframeIndicatorIsNotAnOrdinaryTOC(t *testing.T) {
	envelopes := 0
	for b := 0; b < 256; b++ {
		if !isMultiframe([]byte{byte(b), 0x01, 0xaa}) {
			continue
		}
		envelopes++
		toc := ParseSmplTOC(byte(b))
		if !toc.SID || toc.StdOpus || b&0x02 == 0 {
			t.Fatalf("envelope indicator %#02x would otherwise be a decodable frame", b)
		}
	}
	if envelopes != 32 {
		t.Fatalf("%d envelope indicators, want 32 (bits 7 and 1 set, bit 6 clear)", envelopes)
	}
}

// How the shipped client reaches a packet longer than its frame length: two
// 60 ms blocks in one envelope. Read as a TOC the indicator is a SID, and every
// packet of such a call would become comfort noise.
func TestAMultiframeEnvelopeDecodesInsteadOfBecomingComfortNoise(t *testing.T) {
	enc := NewMlowEncoder()
	tone := make([]float32, opusFrameSamps)
	for i := range tone {
		tone[i] = float32(0.3 * math.Sin(float64(i)*0.05))
	}
	first, err := enc.Encode(tone)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	second, err := enc.Encode(tone)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	envelope := multiframeEnvelope(first, second)
	if !ParseSmplTOC(envelope[0]).SID {
		t.Fatal("the envelope indicator no longer reads as a SID; this test lost its point")
	}

	for _, redundancy := range []int{0, 1} {
		payload := envelope
		if redundancy > 0 {
			// The redundancy wrapper is chosen by the payload type, and the
			// frame inside it can still be an envelope: main marker, then it.
			payload = append([]byte{0x00}, envelope...)
		}
		dec := NewMlowDecoder()
		dec.SetRedundancy(redundancy)
		pcm := dec.Decode(payload)
		report := dec.TakeFrameReport()
		if report.Decoded != 6 || report.InactiveOrSID != 0 || report.Concealed != 0 {
			t.Fatalf("redundancy %d: report = %+v, want six decoded internal frames", redundancy, report)
		}
		if len(pcm) != 2*opusFrameSamps || allZero(pcm) {
			t.Fatalf("redundancy %d: envelope decoded to %d samples (silent %v)", redundancy, len(pcm), allZero(pcm))
		}
	}

	// A malformed envelope is concealed and counted, never mistaken for comfort noise.
	dec := NewMlowDecoder()
	pcm := dec.Decode(append([]byte{0x92, 0x02, 10}, make([]byte, 10)...))
	if report := dec.TakeFrameReport(); report.Concealed != 1 || report.InactiveOrSID != 0 || !allZero(pcm) {
		t.Fatalf("malformed envelope report = %+v", report)
	}
	// So is one whose sub-frames total more than 120 ms.
	pcm = dec.Decode(multiframeEnvelope(first, second, first))
	if report := dec.TakeFrameReport(); report.Concealed != 1 || report.Decoded != 0 || !allZero(pcm) {
		t.Fatalf("180 ms envelope report = %+v", report)
	}
}

// A real capture decodes with nothing concealed: the endpoint check must not
// reject audio the reference plays.
func TestRealStreamsAreNeverConcealed(t *testing.T) {
	for _, name := range []string{"inbound_capture_frames.json", "mlow_dtx_off_frames.json"} {
		dec := NewMlowDecoder()
		var total FrameReport
		for _, frame := range loadHexFrames(t, name) {
			dec.Decode(frame)
			r := dec.TakeFrameReport()
			total.Decoded += r.Decoded
			total.Concealed += r.Concealed
			total.OffPoint += r.OffPoint
			total.InactiveOrSID += r.InactiveOrSID
		}
		t.Logf("%s: %+v", name, total)
		if total.Concealed != 0 || total.OffPoint != 0 || total.Decoded == 0 {
			t.Fatalf("%s: report = %+v, want nothing concealed or refused", name, total)
		}
	}
}
