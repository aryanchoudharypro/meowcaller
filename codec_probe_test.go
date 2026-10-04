package meowcaller

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"testing"

	"github.com/purpshell/meowcaller/rtp"
)

func TestOpusPacketSamples(t *testing.T) {
	body := make([]byte, 40)
	for _, tc := range []struct {
		name    string
		payload []byte
		want    uint32
		ok      bool
	}{
		// 0x58 is config 11, SILK wideband 60 ms, code 0: what WhatsApp Desktop sends.
		{"silk wb 60 ms", append([]byte{0x58}, body...), 960, true},
		// The same byte MLow reads as 60 ms (0x50) is 40 ms of Opus.
		{"mlow 60 ms toc", append([]byte{0x50}, body...), 640, true},
		{"mlow inactive toc", append([]byte{0x10}, body...), 640, true},
		{"code 1 odd body", append([]byte{0x59}, body[:39]...), 0, false},
		{"code 1 even body", append([]byte{0x49}, body...), 640, true},
		{"code 2 length past the end", []byte{0x4a, 200, 1, 2}, 0, false},
		{"code 3 zero frames", []byte{0x4b, 0x00, 1, 2}, 0, false},
		{"code 3 cbr three 20 ms frames", append([]byte{0x4b, 0x03}, body[:39]...), 960, true},
		{"code 3 cbr uneven", append([]byte{0x4b, 0x03}, body...), 0, false},
		{"code 3 vbr lengths past the end", []byte{0x4b, 0x83}, 0, false},
		{"code 3 padding past the end", []byte{0x4b, 0x41, 9, 1}, 0, false},
		{"past 120 ms", append([]byte{0x5b, 0x03}, body[:39]...), 0, false},
		{"celt 2.5 ms", append([]byte{0x80}, body...), 40, true},
		{"empty", nil, 0, false},
	} {
		got, ok := opusPacketSamples(tc.payload, SampleRate)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: opusPacketSamples = (%d, %v), want (%d, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestInboundCodecProbeNeedsThreeAgreeingPacketsAtTheNegotiatedCadence(t *testing.T) {
	opus := append([]byte{0x58}, make([]byte, 60)...)
	var probe inboundCodecProbe
	sequence, timestamp := uint16(65534), uint32(1000)
	feed := func(payload []byte, step uint32) (agrees, settled bool) {
		sequence++
		timestamp += step
		return probe.observe(payload, sequence, timestamp)
	}
	// The first packet has no step to cross-check against.
	if agrees, settled := feed(opus, 960); agrees || settled {
		t.Fatal("a packet with no previous step counted as evidence")
	}
	for i := 0; i < 2; i++ {
		if agrees, settled := feed(opus, 960); !agrees || settled {
			t.Fatalf("agreeing packet %d = (%v, %v), want (true, false)", i, agrees, settled)
		}
	}
	if agrees, settled := feed(opus, 960); !agrees || !settled || !probe.opus {
		t.Fatal("three agreeing packets at the negotiated cadence did not settle the stream as Opus")
	}
	if _, settled := feed(opus, 960); settled {
		t.Fatal("the verdict was reported twice")
	}

	// A step other than the negotiated one is no evidence, and breaks the streak.
	probe = inboundCodecProbe{}
	feed(opus, 960)
	feed(opus, 960)
	feed(opus, 960)
	if agrees, _ := feed(opus, 1920); agrees || probe.agreeing != 0 {
		t.Fatal("a broken cadence did not reset the streak")
	}
	feed(opus, 960)
	feed(opus, 960)
	if _, settled := feed(opus, 960); !settled {
		t.Fatal("the streak did not recover")
	}

	// A lost packet leaves no step to compare: abstain, without resetting.
	probe = inboundCodecProbe{}
	feed(opus, 960)
	feed(opus, 960)
	feed(opus, 960)
	sequence++
	if agrees, _ := feed(opus, 1920); agrees || probe.agreeing != 2 {
		t.Fatalf("a sequence gap changed the streak to %d", probe.agreeing)
	}
}

func loadFixtureFrames(t *testing.T, path string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var frames []string
	if err = json.Unmarshal(raw, &frames); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := make([][]byte, 0, len(frames))
	for _, hf := range frames {
		payload, decodeErr := hex.DecodeString(hf)
		if decodeErr != nil {
			t.Fatalf("%s: bad hex: %v", path, decodeErr)
		}
		out = append(out, payload)
	}
	return out
}

// Genuine MLow can never be promoted: at the negotiated 960-sample step every
// TOC the MLow decoder accepts declares some other Opus duration.
func TestInboundCodecProbeNeverPromotesMlow(t *testing.T) {
	body := make([]byte, 60)
	for toc := 0; toc < 256; toc++ {
		// Bit 7 clear (not a SID), bit 5 clear (16 kHz), bit 2 clear, 20 or 60 ms.
		// (A single-block 120 ms packet does read as 60 ms of Opus, which is the
		// collision itself; it paces at 1920 samples, so the cadence gate holds.)
		if toc&0xa4 != 0 || toc&0x18 == 0 || toc&0x18 == 0x18 {
			continue
		}
		for length := 1; length <= len(body); length++ {
			payload := append([]byte{byte(toc)}, body[:length]...)
			if declared, ok := opusPacketSamples(payload, SampleRate); ok && declared == FrameSamples {
				t.Fatalf("MLow TOC %#02x with a %d-byte body reads as 60 ms of Opus", toc, length)
			}
		}
	}

	var probe inboundCodecProbe
	for i, payload := range loadFixtureFrames(t, "mlow/testdata/inbound_capture_frames.json") {
		if len(payload) == 0 {
			continue
		}
		if agrees, settled := probe.observe(payload, uint16(i), uint32(i)*FrameSamples); agrees || settled || probe.opus {
			t.Fatalf("a real MLow capture read as Opus at frame %d", i)
		}
	}
}

// goertzelPower is the signal power at one frequency.
func goertzelPower(pcm []float32, hz float64) float64 {
	w := 2 * math.Pi * hz / SampleRate
	coeff := 2 * math.Cos(w)
	var s1, s2 float64
	for _, sample := range pcm {
		s0 := float64(sample) + coeff*s1 - s2
		s2, s1 = s1, s0
	}
	return (s1*s1 + s2*s2 - coeff*s1*s2) / float64(len(pcm))
}

// A peer outside the MLow rollout sends standard Opus on the shared payload
// type (WhatsApp Desktop does: 60 ms SILK wideband, TOC 0x58, which MLow reads
// as a 120 ms packet). Its packets go to the Opus decoder, not the MLow one.
// The fixture is libopus at the shape the shipped client uses: 16 kHz mono,
// 60 ms, 24 kbit/s, a 220 Hz + 1100 Hz tone.
func TestDecodeAudioDecodesAStandardOpusPeer(t *testing.T) {
	callKey := make([]byte, 32)
	for i := range callKey {
		callKey[i] = byte(i + 1)
	}
	self, peer := mediaTestJID("100", 1), mediaTestJID("200", 2)
	mlowDecoder := &recordingParticipantDecoder{}
	registry, err := newParticipantReceiveRegistry(
		"CID", callKey, self.String(), peer.String(),
		func() participantAudioDecoder { return mlowDecoder },
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	participantID := rtp.FormatE2ESrtpParticipantID(peer.String())
	ssrc, err := rtp.DeriveWasmParticipantSsrc("CID", participantID, 0)
	if err != nil {
		t.Fatalf("derive sender SSRC: %v", err)
	}
	sender, err := NewMediaPipeline(callKey, peer.String(), self.String(), ssrc, FrameSamples)
	if err != nil {
		t.Fatalf("sender pipeline: %v", err)
	}
	frames := loadFixtureFrames(t, "testdata/opus_silk_wb_60ms_frames.json")
	// A DTX packet in the middle of the stream is a silent slot, not an error.
	frames = append(frames[:20:20], append([][]byte{OpusSilencePacket}, frames[20:]...)...)
	var pcm []float32
	for i, frame := range frames {
		if frame[0] != 0x58 {
			t.Fatalf("fixture frame %d is TOC %#02x, not 60 ms SILK wideband", i, frame[0])
		}
		packet, protectErr := sender.ProtectAudio(frame)
		if protectErr != nil {
			t.Fatalf("protect packet %d: %v", i, protectErr)
		}
		audio, ok := registry.DecodeAudio(packet)
		if !ok {
			t.Fatalf("packet %d did not authenticate", i)
		}
		// The first packet has no timestamp step to cross-check, so it alone
		// is still read as MLow; the third agreeing step, on packet 3, settles
		// the stream.
		if audio.StandardOpus != (i >= 1) || audio.StandardOpusSettled != (i >= 3) {
			t.Fatalf("packet %d StandardOpus = %v, settled = %v", i, audio.StandardOpus, audio.StandardOpusSettled)
		}
		if !audio.StandardOpus {
			continue
		}
		if audio.Report.Decoded != 1 || audio.Report.Concealed != 0 || len(audio.PCM) != FrameSamples {
			t.Fatalf("packet %d report = %+v with %d samples", i, audio.Report, len(audio.PCM))
		}
		pcm = append(pcm, audio.PCM...)
	}
	if len(mlowDecoder.payloads) != 1 {
		t.Fatalf("the MLow decoder saw %d packets, want only the first", len(mlowDecoder.payloads))
	}
	steady := pcm[4*FrameSamples : 16*FrameSamples]
	var energy float64
	for _, sample := range steady {
		energy += float64(sample) * float64(sample)
	}
	rms := math.Sqrt(energy / float64(len(steady)))
	tone, off := goertzelPower(steady, 220), goertzelPower(steady, 600)
	t.Logf("decoded rms %.4f, power at 220 Hz %.3f vs 600 Hz %.6f", rms, tone, off)
	if rms < 0.02 || tone < 100*off {
		t.Fatalf("decoded Opus is not the tone that was encoded (rms %.4f, 220 Hz %.4f, 600 Hz %.4f)", rms, tone, off)
	}
}

type opusFrameTestSource struct {
	frames [][]float32
	opus   [][]byte
	last   []byte
}

func (s *opusFrameTestSource) ReadFrame() ([]float32, error) {
	if len(s.frames) == 0 {
		return nil, io.EOF
	}
	frame := s.frames[0]
	s.frames, s.last, s.opus = s.frames[1:], s.opus[0], s.opus[1:]
	return frame, nil
}

func (s *opusFrameTestSource) OpusFrame() []byte { return s.last }
func (s *opusFrameTestSource) Close() error      { return nil }

func TestPlayerHandsOverTheSourcesOpusEncoding(t *testing.T) {
	encoded := []byte{0x58, 1, 2, 3}
	player := NewPlayer()
	player.Play(&opusFrameTestSource{
		frames: [][]float32{make([]float32, FrameSamples), make([]float32, FrameSamples)},
		opus:   [][]byte{encoded, nil},
	})
	frame, opusFrame := player.nextFrameOpus()
	if len(frame) != FrameSamples || string(opusFrame) != string(encoded) {
		t.Fatalf("first frame = %d samples with Opus %x", len(frame), opusFrame)
	}
	if got := standardOpusPayload(opusFrame); string(got) != string(encoded) {
		t.Fatalf("payload = %x, want the source's encoding", got)
	}
	// A frame the source could not encode goes out as a silent Opus packet,
	// never as MLow bytes the peer would decode as noise.
	frame, opusFrame = player.nextFrameOpus()
	if len(frame) != FrameSamples || opusFrame != nil {
		t.Fatalf("second frame = %d samples with Opus %x", len(frame), opusFrame)
	}
	if got := standardOpusPayload(opusFrame); string(got) != string(OpusSilencePacket) {
		t.Fatalf("payload = %x, want the silent packet", got)
	}
	if samples, ok := opusPacketSamples(OpusSilencePacket, SampleRate); !ok || samples != FrameSamples {
		t.Fatalf("the silent packet declares %d samples (%v), want one 60 ms frame", samples, ok)
	}
	// A plain PCM source has no encoding to hand over.
	player.Play(PCMStream(io.NopCloser(bytes.NewReader(make([]byte, FrameSamples*2)))))
	if frame, opusFrame = player.nextFrameOpus(); len(frame) != FrameSamples || opusFrame != nil {
		t.Fatalf("PCM source frame = %d samples with Opus %x", len(frame), opusFrame)
	}
}

func TestCallSendsStandardOpusOnNegotiationOrEvidence(t *testing.T) {
	var none *Call
	call := &Call{}
	if none.sendsStandardOpus() || call.sendsStandardOpus() {
		t.Fatal("a call is on standard Opus by default")
	}
	call.negotiatedOpus.Store(true)
	if !call.StandardOpus() {
		t.Fatal("a negotiated Opus call does not send Opus")
	}
	call.negotiatedOpus.Store(false)
	call.peerSendsOpus.Store(true)
	if !call.StandardOpus() {
		t.Fatal("a peer found sending Opus is not answered in Opus")
	}
}
