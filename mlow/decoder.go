package mlow

import (
	"errors"

	"github.com/rs/zerolog"
)

// MLow top-level decoder: RED strip → multiframe envelope → TOC routing →
// active-frame decode (chained 20 ms internal frames: LSF → pulses → pitch/gains →
// reconstruct → CELP synthesis) → per-packet harmonic postfilter → PCM. Cross-frame
// predictor and synthesis history persist across calls (the stream is continuous).
//
// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip/mlow/decoder.rs#L1-L537

const (
	opusFrameSamps = 960 // 60 ms @ 16 kHz
	// outputSamplesPerMs: the decoder synthesises at 16 kHz regardless of the
	// internal rate a TOC declares.
	outputSamplesPerMs = 16
	// maxPacketSamps is the longest audio one packet may carry: 120 ms, both the
	// longest duration a TOC can declare and the ceiling the shipped client
	// applies when it totals a multiframe envelope. Without it an envelope of
	// eighteen sub-frames turns a few hundred bytes into tens of thousands of
	// samples the playout buffer cannot tell from real audio.
	maxPacketSamps = 1920
)

// silenceSamps is how many samples a frame that produced no audio occupies: its
// own declared duration, not a fixed 60 ms slot. A fixed slot makes a run of
// concealed 20 ms frames arrive at three times real time.
func silenceSamps(declared int) int {
	return min(max(declared, 1), maxPacketSamps)
}

// internalFrames is how many 20 ms internal frames one packet of frameMs chains,
// or 0 for a duration this decoder cannot run. The geometry inside each
// iteration stays fixed, so 20/60/120 ms differ only in how many times the same
// decode repeats. 10 ms halves the internal frame length and the subframe count,
// which the synthesis does not implement.
func internalFrames(frameMs int) int {
	if frameMs <= 10 {
		return 0
	}
	return (frameMs + 10) / 20
}

// endpointIsValid reports whether a decode that ended after consumed bytes of a
// storage-byte body landed where a valid stream can end. Under-running is always
// malformed; the upper slack absorbs the range coder's final carry bytes, which
// the encoder does not emit. The bound of four is the shipped decoder's.
func endpointIsValid(storage, consumed uint32) bool {
	return storage <= consumed && consumed <= storage+4
}

// FrameReport is what one Decode call did with the packet it was given. The
// decoder answers with PCM either way, so without it a concealed frame, a frame
// outside the operating point and a frame of genuine background noise are the
// same observation: silence. Counts, because one packet chains several internal
// frames and an envelope chains several packets.
type FrameReport struct {
	// Decoded is internal frames that produced coded audio.
	Decoded uint32
	// Concealed is frames replaced by silence: an empty payload, a RED or
	// multiframe envelope that would not unwrap, or a body whose decode did not
	// end where the body said it should.
	Concealed uint32
	// OffPoint is frames refused by the operating-point guard, including a
	// standard-Opus escape this decoder does not run.
	OffPoint uint32
	// InactiveOrSID is frames that carried no coded voice (SID / comfort noise,
	// or the short off-point startup silence).
	InactiveOrSID uint32
}

// SmplDecoderState is the cross-frame decoder state: LSF predictor, previous NLSF,
// the CELP synthesis state, and the harmonic-postfilter state.
type SmplDecoderState struct {
	Lstate   SmplLsfState
	PrevNLSF []float32
	Celp     *CelpDecState
	Harm     *HarmPostfilterState
}

func newSmplDecoderState() *SmplDecoderState {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_synth.rs#L641-L672
	return &SmplDecoderState{Celp: NewCelpDecState(), Harm: NewHarmPostfilterState()}
}

// smplDecodeRollback is a snapshot of the part of SmplDecoderState an active
// decode advances, so a malformed body can be undone. The harmonic postfilter is
// left out: it runs after the endpoint check, so a concealed packet never
// advanced it. Owned by the decoder and reused so the copy does not allocate.
type smplDecodeRollback struct {
	lstate   SmplLsfState
	prevNLSF []float32
	hasNLSF  bool
	celp     CelpDecState
}

func (r *smplDecodeRollback) save(state *SmplDecoderState) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip/mlow/smpl_synth.rs#L600-L606
	r.lstate = state.Lstate
	r.prevNLSF = append(r.prevNLSF[:0], state.PrevNLSF...)
	r.hasNLSF = state.PrevNLSF != nil
	acbState, xOld := r.celp.acbState, r.celp.hp.xOld
	r.celp = *state.Celp
	r.celp.acbState = append(acbState[:0], state.Celp.acbState...)
	r.celp.hp.xOld = append(xOld[:0], state.Celp.hp.xOld...)
	r.celp.ExcPre = nil
}

func (r *smplDecodeRollback) restore(state *SmplDecoderState) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip/mlow/smpl_synth.rs#L608-L613
	state.Lstate = r.lstate
	state.PrevNLSF = nil
	if r.hasNLSF {
		state.PrevNLSF = append([]float32{}, r.prevNLSF...)
	}
	live := state.Celp
	acbState, xOld, excPre := live.acbState, live.hp.xOld, live.ExcPre
	*live = r.celp
	live.acbState = append(acbState[:0], r.celp.acbState...)
	live.hp.xOld = append(xOld[:0], r.celp.hp.xOld...)
	live.ExcPre = excPre
}

// MlowDecoder is a stateful pure-Go MLow decoder.
type MlowDecoder struct {
	state              *SmplDecoderState
	stateBackup        smplDecodeRollback
	redundancy         int32
	droppedUnsupported uint32
	// malformed counts frames concealed because the decode did not end where
	// the body said it should, or an envelope that would not split. Drives a
	// once + every-100th warning.
	malformed uint32
	// report is what the in-flight Decode call has done so far.
	report FrameReport
	log    zerolog.Logger
}

// NewMlowDecoder allocates a fresh decoder.
func NewMlowDecoder(opts ...Option) *MlowDecoder {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/decoder.rs#L36-L41
	return &MlowDecoder{state: newSmplDecoderState(), log: resolveConfig(opts).log}
}

// SetRedundancy sets the negotiated RED redundancy level (0 = bare frames).
func (d *MlowDecoder) SetRedundancy(n int) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/decoder.rs#L44-L46
	d.redundancy = int32(n)
}

// Reset clears the cross-frame state (call at a stream discontinuity).
func (d *MlowDecoder) Reset() {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/decoder.rs#L49-L51
	d.state = newSmplDecoderState()
}

// TakeFrameReport returns what the last Decode did, clearing it.
func (d *MlowDecoder) TakeFrameReport() FrameReport {
	report := d.report
	d.report = FrameReport{}
	return report
}

// Decode decodes one RTP MLow payload into PCM, float in [-1, 1]. The sample
// count follows the packet's declared duration (20, 60 or 120 ms; several
// packets for a multiframe envelope).
func (d *MlowDecoder) Decode(payload []byte) []float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip/mlow/decoder.rs#L181-L211
	d.report = FrameReport{}
	if len(payload) == 0 {
		d.log.Trace().Msg("decode: empty payload, emitting silence")
		d.report.Concealed++
		return make([]float32, opusFrameSamps)
	}
	d.log.Trace().Int("payload_bytes", len(payload)).Int32("redundancy", d.redundancy).Msg("decode packet")
	if d.redundancy > 0 {
		frames, err := DepackSplitRed(payload, d.log)
		if err != nil {
			d.log.Debug().Err(err).Int("payload_bytes", len(payload)).Msg("decode: RED depack failed, emitting silence")
			d.report.Concealed++
			return make([]float32, opusFrameSamps)
		}
		var main []byte
		if len(frames) > 0 {
			main = frames[len(frames)-1].Data // the main (current) frame is last
		}
		d.log.Trace().Int("red_frames", len(frames)).Int("main_bytes", len(main)).Msg("decode: RED depacked")
		// Through decodeInner, not decodeFrame: the redundancy wrapper is chosen
		// by the payload type, and nothing stops the frame inside it from being
		// a multiframe envelope.
		return d.decodeInner(main)
	}
	return d.decodeInner(payload)
}

// decodeInner routes one bare MLow payload: multiframe envelope, or a single
// frame. The envelope is checked before the TOC is read, because under the TOC
// grammar its first byte reads as a SID.
func (d *MlowDecoder) decodeInner(payload []byte) []float32 {
	if isMultiframe(payload) {
		return d.decodeMultiframe(payload)
	}
	return d.decodeFrame(payload)
}

// decodeMultiframe decodes every sub-frame of an envelope, in transmission
// order, into one contiguous packet.
func (d *MlowDecoder) decodeMultiframe(payload []byte) []float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip/mlow/decoder.rs#L228-L275
	frames, err := splitMultiframe(payload)
	if err == nil {
		// Refuse before decoding anything: a packet whose sub-frames total more
		// than the format allows is malformed as a whole, and half-decoding it
		// would leave the predictor advanced by frames the peer never meant as
		// one packet.
		declared := 0
		for _, frame := range frames {
			declared += silenceSamps(outputSamplesPerMs * ParseSmplTOC(frame[0]).FrameMs)
		}
		if declared <= maxPacketSamps {
			d.log.Trace().Int("sub_frames", len(frames)).Int("payload_bytes", len(payload)).Msg("decode: multiframe envelope")
			out := make([]float32, 0, declared)
			for _, frame := range frames {
				out = append(out, d.decodeFrame(frame)...)
			}
			return out
		}
		err = errors.New("mlow multiframe: envelope declares more than 120 ms")
	}
	d.malformed++
	if d.malformed == 1 || d.malformed%100 == 0 {
		d.log.Warn().Err(err).Uint32("malformed", d.malformed).Uint8("indicator", payload[0]).
			Msg("concealing unreadable MLow multiframe packet")
	}
	d.report.Concealed++
	return make([]float32, opusFrameSamps)
}

func (d *MlowDecoder) decodeFrame(frame []byte) []float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip/mlow/decoder.rs#L277-L345
	if len(frame) == 0 {
		d.log.Trace().Msg("decode frame: empty, emitting silence")
		d.report.Concealed++
		return make([]float32, opusFrameSamps)
	}
	toc := ParseSmplTOC(frame[0], d.log)
	// In OUTPUT samples, always 16 kHz: a frame declaring a 32 kHz internal rate
	// still occupies its duration's worth of 16 kHz samples.
	declared := silenceSamps(outputSamplesPerMs * toc.FrameMs)
	frames := internalFrames(toc.FrameMs)
	d.log.Trace().Int("frame_bytes", len(frame)).Uint8("toc_byte", frame[0]).
		Bool("std_opus", toc.StdOpus).Bool("sid", toc.SID).Bool("active", toc.Active).
		Bool("voiced", toc.Voiced).Int("frame_ms", toc.FrameMs).Int("sample_rate", toc.SampleRate).
		Int("internal_frames", frames).Msg("decode frame")
	if toc.StdOpus {
		d.log.Debug().Msg("decode frame: standard-Opus packet, not handled, emitting silence")
		d.report.OffPoint++
		return make([]float32, declared)
	}
	// A SID (DTX/CNG) frame carries comfort noise rather than coded voice and is
	// silenced without opening the range coder, so its geometry can never desync.
	// Handled before the operating-point guard so an off-point SID reads as the
	// benign silence it is instead of tripping the "dropped" canary below.
	//
	// A frame that is merely coded inactive is NOT silence: with DTX off the
	// encoder keeps sending background noise this way and the reference decodes
	// it. Silencing it drops every pause of such a peer on the floor, which does
	// not sound broken, it sounds like a slightly dead line.
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/pull/1113
	if toc.SID {
		d.log.Trace().Bool("sid", toc.SID).Msg("decode frame: SID, emitting silence")
		d.report.InactiveOrSID++
		return make([]float32, declared)
	}
	offPoint := toc.SampleRate != 16000 || frames == 0
	// An inactive frame that is also off the operating point is not DTX-off
	// background noise — it is the short startup silence a real peer emits before
	// speech (10 ms in the captured stream). It has never been decodable, so keep
	// silencing it at its nominal length: routing it onward would only trip the
	// "dropped" canary below.
	if !toc.Active && offPoint {
		d.log.Trace().Int("frame_ms", toc.FrameMs).Int("sample_rate", toc.SampleRate).
			Msg("decode frame: inactive off-point, emitting silence")
		d.report.InactiveOrSID++
		return make([]float32, declared)
	}
	if offPoint {
		d.droppedUnsupported++
		if d.droppedUnsupported == 1 || d.droppedUnsupported%100 == 0 {
			d.log.Warn().
				Uint32("dropped", d.droppedUnsupported).
				Uint8("toc_byte", frame[0]).
				Int("sample_rate", toc.SampleRate).
				Bool("low_rate", toc.Flag2).
				Int("frame_ms", toc.FrameMs).
				Msg("dropping unsupported active MLow frame")
		}
		d.report.OffPoint++
		return make([]float32, declared)
	}
	return d.decodeActiveFrame(frame, frames*SmplIntfLen, frames, toc.Active)
}

// decodeActiveFrame decodes a non-SID frame. codedAsActiveVoice is the TOC's
// active-voice bit: false means the frame carries background noise coded without
// the voicing and interpolation symbols, and it must be threaded into every read
// that is gated on them.
func (d *MlowDecoder) decodeActiveFrame(frame []byte, outLen, frames int, codedAsActiveVoice bool) []float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/decoder.rs#L101-L217
	config := int(frame[0]>>2) & 1
	tbl := LoadSmplTables()
	synthT := LoadSmplSynthTables()
	mem := LoadSmplMem()
	dec := NewRangeDecoder(frame[1:])
	lowRate := (frame[0]>>2)&1 != 0
	numSubframes := int32(4)
	if lowRate {
		numSubframes = 2
	}

	d.log.Trace().Int("config", config).Bool("low_rate", lowRate).Int("body_bytes", len(frame)-1).Int("internal_frames", frames).Msg("decode active frame")

	// The overrun that invalidates a body is only detectable after the last
	// internal frame, by which point the loop has already advanced the LSF
	// predictor, the CELP history and PrevNLSF. Keep a copy so concealment can
	// undo them: parameters invented past the end of a bad body must not seed
	// the next packet.
	d.stateBackup.save(d.state)

	out := make([]float32, 0, frames*SmplIntfLen)
	packetLags := make([]float32, 0, frames*8)
	var avgNormBr float32
	for f := 0; f < frames; f++ {
		lsf := DecodeSmplLsf(dec, tbl, &d.state.Lstate, config, f, codedAsActiveVoice)
		activeVoice := int32(0)
		if codedAsActiveVoice {
			activeVoice = 1
		}
		pulses := DecodeSmplPulses(dec, mem, SmplIntfLen, numSubframes, activeVoice, int32(config), lsf.Stage1)
		voiced := lsf.Stage1 == 1
		var total int32
		for _, c := range pulses.Subfr {
			total += c
		}
		params := CelpDecParams{Voiced: voiced, SfPulses: pulses.Subfr, TotalPulses: total}
		if voiced {
			pr := DecodeSmplPitch(dec, mem, &d.state.Lstate, SmplIntfLen, numSubframes, int32(config), pulses.Subfr)
			for b := 0; b < 8; b++ {
				v := float64(pr.BlockLags[b])*0.5 + 32.0
				if v > 320.0 {
					v = 320.0
				}
				params.BlockLags[b] = float32(v)
			}
			for sf := 0; sf < int(numSubframes); sf++ {
				params.AcbgIdx[sf] = pr.GainIdx[sf]
				if pr.FiltIdx[sf] > 0 {
					params.FcbgIdx[sf] = pr.FiltIdx[sf]
				}
			}
		} else {
			g := DecodeSmplGains(dec, mem, numSubframes, pulses.Subfr)
			params.NrgresDbqQ14 = g.GainQ
			params.FcbgIdx = g.NrgRes
		}
		packetLags = append(packetLags, params.BlockLags[:]...)
		avgNormBr += SmplGetNormalizedBitrate(params.TotalPulses, SmplIntfLen)

		d.log.Trace().Int("intf", f).Bool("voiced", voiced).Int32("stage1", lsf.Stage1).
			Int32("grid", lsf.Grid).Int32("total_pulses", total).Int("nlsf_len", len(d.state.PrevNLSF)).
			Msg("decode internal frame params")

		nlsf := SmplReconstructNLSF(synthT, int(lsf.Stage1), config, int(lsf.Grid), &lsf.Stage2, d.state.PrevNLSF)
		var sig [SmplIntfLen]float32
		d.state.Celp.SynthFrame(nlsf, int(lsf.Extra), pulses.Pulses, &params, lowRate, SmplIntfLen, sig[:])
		d.state.PrevNLSF = nlsf
		out = append(out, sig[:]...)
	}
	// Endpoint check, before the postfilter as in the reference: the range decoder
	// returns zero past either end of its storage WITHOUT flagging it, so an
	// impossible stream decodes into plausible-looking symbols and the synthesis
	// can diverge to full scale. Comparing where the decode ended against what
	// the body actually held is the only way to see it. Anything outside the
	// accepted window is a malformed frame, concealed as a lost one rather than
	// synthesized, with the state the loop advanced rolled back.
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip/mlow/decoder.rs#L468-L503
	consumedBytes := (uint32(max(dec.Tell(), 0)) + 7) / 8
	if body := dec.Storage(); !endpointIsValid(body, consumedBytes) || dec.Err != 0 {
		d.stateBackup.restore(d.state)
		d.malformed++
		if d.malformed == 1 || d.malformed%100 == 0 {
			d.log.Warn().Uint32("malformed", d.malformed).Uint8("toc_byte", frame[0]).
				Uint32("consumed_bytes", consumedBytes).Uint32("body_bytes", body).
				Int32("range_error", dec.Err).Msg("concealing malformed MLow frame")
		}
		d.report.Concealed++
		return make([]float32, outLen)
	}
	d.report.Decoded += uint32(frames)

	// Per-packet harmonic postfilter (final pitch comb + 48-sample group delay) over the whole packet.
	plen := len(out)
	d.log.Trace().Int("samples", plen).Int("packet_lags", len(packetLags)).Msg("decode active frame: applying harmonic postfilter")
	SmplHarmPostfilter(d.state.Harm, out, plen, packetLags, len(packetLags), avgNormBr/float32(frames))

	pcm := make([]float32, len(out))
	for i, v := range out {
		switch {
		case v > 1.0:
			v = 1.0
		case v < -1.0:
			v = -1.0
		}
		pcm[i] = v
	}
	if outLen > 0 && outLen != len(pcm) {
		if outLen <= len(pcm) {
			pcm = pcm[:outLen]
		} else {
			np := make([]float32, outLen)
			copy(np, pcm)
			pcm = np
		}
	}
	return pcm
}
