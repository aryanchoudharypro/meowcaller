package meowcaller

import (
	"fmt"
	"runtime"

	opus "github.com/kazzmir/opus-go/opus"
)

// opusMaxPacketSamples is the most audio one Opus packet can carry at 16 kHz
// (120 ms).
const opusMaxPacketSamples = opusMaxPacketDurationUs / 1000 * SampleRate / 1000

// OpusSilencePacket is a standard Opus packet that carries no audio: the TOC of
// a 60 ms SILK wideband frame and nothing after it, which a decoder reads as a
// lost or DTX frame. It is what a call on standard Opus sends while it has no
// encoded frame to send, so the stream keeps its cadence.
var OpusSilencePacket = []byte{0x58}

// standardOpusDecoder decodes RFC 6716 Opus to 16 kHz mono, for a peer outside
// the MLow rollout: the payload type carries either codec. It is libopus (via
// a cgo-free port), because only libopus recovers a lost packet from the next
// one's in-band FEC and conceals longer losses the way WhatsApp's own decoder
// does.
type standardOpusDecoder struct {
	dec *opus.Decoder
	buf []float32
	// lastEnd is the RTP timestamp just after the last packet decoded.
	lastEnd uint32
	hasLast bool
}

func newStandardOpusDecoder() (*standardOpusDecoder, error) {
	dec, err := opus.NewDecoder(SampleRate, 1)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: create Opus decoder: %w", err)
	}
	d := &standardOpusDecoder{dec: dec, buf: make([]float32, opusMaxPacketSamples)}
	runtime.SetFinalizer(d, func(d *standardOpusDecoder) { _ = d.dec.Close() })
	return d, nil
}

// maxConcealedFrames bounds how many lost packets are filled in front of a
// packet that arrives after a gap.
const maxConcealedFrames = 3

// decode returns the packet's audio, or ok=false when it would not decode.
func (d *standardOpusDecoder) decode(payload []byte) (pcm []float32, ok bool) {
	pcm, _, ok = d.decodeAt(payload, 0)
	return pcm, ok
}

// decodeAt decodes the packet that starts at RTP timestamp ts. When packets
// went missing just before it, the audio returned starts earlier, at
// ts-lead, and begins with what libopus recovers for them: the packet's own
// in-band FEC for one lost packet, concealment for up to maxConcealedFrames.
//
// libopus is a C port with its assertions compiled in, and a packet that only
// looks like Opus can trip one, which surfaces as a panic. One bad packet
// must not end the call, so the panic is caught, the packet dropped, and the
// decoder replaced, since its state is no longer trustworthy.
func (d *standardOpusDecoder) decodeAt(payload []byte, ts uint32) (pcm []float32, lead uint32, ok bool) {
	defer func() {
		if recover() != nil {
			pcm, lead, ok = nil, 0, false
			// The aborted decoder is abandoned, not closed: the abort left its
			// pseudo-stack mid-call, and freeing that through the port's
			// shared allocator can break the decoders that come after it. It
			// is a few kilobytes, once per bad packet.
			if fresh, err := opus.NewDecoder(SampleRate, 1); err == nil {
				d.dec = fresh
			}
			d.hasLast = false
		}
	}()
	return d.decodeAtUnguarded(payload, ts)
}

func (d *standardOpusDecoder) decodeAtUnguarded(payload []byte, ts uint32) (pcm []float32, lead uint32, ok bool) {
	samples, valid := opusPacketSamples(payload, SampleRate)
	frame := int(samples)
	if !valid || frame <= 0 || frame > len(d.buf) {
		return nil, 0, false
	}
	var out []float32
	if d.hasLast {
		if gap := int32(ts - d.lastEnd); gap > 0 && int(gap) <= maxConcealedFrames*frame {
			lost := (int(gap) + frame - 1) / frame
			for i := 0; i < lost; i++ {
				useFEC := i == lost-1
				source := []byte(nil)
				if useFEC {
					source = payload
				}
				n, err := d.dec.DecodeF32(source, d.buf, frame, useFEC)
				if err != nil || n <= 0 {
					break
				}
				out = append(out, d.buf[:n]...)
			}
			lead = uint32(len(out))
		}
	}
	n, err := d.dec.DecodeF32(payload, d.buf, frame, false)
	if err != nil || n <= 0 {
		return nil, 0, false
	}
	d.lastEnd, d.hasLast = ts+uint32(n), true
	return append(out, d.buf[:n]...), lead, true
}

// standardOpusPayload is what a call on standard Opus sends for one frame slot:
// the source's own encoding of the frame, or a silent packet when there is none
// (the source cannot encode, or the slot is silence).
func standardOpusPayload(opusFrame []byte) []byte {
	if len(opusFrame) == 0 {
		return OpusSilencePacket
	}
	return opusFrame
}

// sendsStandardOpus reports whether the peer decodes the shared audio payload
// type as standard Opus rather than MLow: the signaling said so, or the peer's
// own packets did.
func (c *Call) sendsStandardOpus() bool {
	return c != nil && (c.negotiatedOpus.Load() || c.peerSendsOpus.Load())
}

// StandardOpus reports whether the call's audio is on standard Opus rather than
// MLow. Such a call is only heard by the peer when the playing source is an
// OpusFrameSource.
func (c *Call) StandardOpus() bool {
	return c.sendsStandardOpus()
}
