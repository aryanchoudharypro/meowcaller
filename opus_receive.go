package meowcaller

import (
	"fmt"

	"github.com/pion/opus"
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
// the MLow rollout: the payload type carries either codec.
type standardOpusDecoder struct {
	dec opus.Decoder
	buf []float32
}

func newStandardOpusDecoder() (*standardOpusDecoder, error) {
	dec, err := opus.NewDecoderWithOutput(SampleRate, 1)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: create Opus decoder: %w", err)
	}
	return &standardOpusDecoder{dec: dec, buf: make([]float32, opusMaxPacketSamples)}, nil
}

// decode returns the packet's audio, or ok=false when it would not decode.
func (d *standardOpusDecoder) decode(payload []byte) (pcm []float32, ok bool) {
	n, err := d.dec.DecodeToFloat32(payload, d.buf)
	if err != nil || n <= 0 {
		return nil, false
	}
	return append([]float32(nil), d.buf[:n]...), true
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
