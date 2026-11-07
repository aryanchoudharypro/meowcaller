package meowcaller

const (
	audioPlayoutPrefillSamples = 2 * FrameSamples
	audioPlayoutMaxGapSamples  = 2 * SampleRate
)

type audioPlayoutFrame struct {
	timestamp uint32
	pcm       []float32
}

// audioPlayoutBuffer converts packet-oriented decoder output into a continuous
// RTP-timestamped PCM stream. Holding two packet intervals gives the downstream
// real-time track enough headroom for normal relay jitter.
type audioPlayoutBuffer struct {
	pending        *audioPlayoutFrame
	prefill        [][]float32
	prefillSamples int
	started        bool
}

func newAudioPlayoutBuffer() *audioPlayoutBuffer {
	return &audioPlayoutBuffer{}
}

func (p *audioPlayoutBuffer) Push(timestamp uint32, frame []float32, sink AudioSink) (startedNow bool, err error) {
	if p.pending == nil {
		p.pending = &audioPlayoutFrame{timestamp: timestamp, pcm: frame}
		return false, nil
	}

	delta := timestamp - p.pending.timestamp
	if delta == 0 || delta > audioPlayoutMaxGapSamples {
		p.reset(timestamp, frame)
		return false, nil
	}

	ready := alignAudioFrame(p.pending.pcm, int(delta))
	p.pending = &audioPlayoutFrame{timestamp: timestamp, pcm: frame}
	if p.started && sink != nil {
		return false, sink.WriteFrame(ready)
	}
	if p.started {
		p.started = false
		p.prefill = nil
		p.prefillSamples = 0
	}

	p.prefill = append(p.prefill, ready)
	p.prefillSamples += len(ready)
	for p.prefillSamples > audioPlayoutMaxGapSamples && len(p.prefill) > 1 {
		p.prefillSamples -= len(p.prefill[0])
		p.prefill = p.prefill[1:]
	}
	if sink == nil || p.prefillSamples < audioPlayoutPrefillSamples {
		return false, nil
	}

	for len(p.prefill) > 0 {
		buffered := p.prefill[0]
		if err = sink.WriteFrame(buffered); err != nil {
			return false, err
		}
		p.prefillSamples -= len(buffered)
		p.prefill = p.prefill[1:]
	}
	p.started = true
	p.prefill = nil
	return true, nil
}

func (p *audioPlayoutBuffer) Flush(sink AudioSink) error {
	if !p.started || p.pending == nil || sink == nil {
		return nil
	}
	err := sink.WriteFrame(p.pending.pcm)
	p.pending = nil
	return err
}

func (p *audioPlayoutBuffer) Drain(sink AudioSink) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/fabad4acce2147da4e40c1e8c6a1643053ae8c59/datasheets/group-audio-mixer.md#L20-L27
	frames := append([][]float32(nil), p.prefill...)
	if p.pending != nil {
		frames = append(frames, p.pending.pcm)
	}
	p.pending = nil
	p.prefill = nil
	p.prefillSamples = 0
	p.started = false
	if sink == nil {
		return nil
	}
	for _, frame := range frames {
		if err := sink.WriteFrame(frame); err != nil {
			return err
		}
	}
	return nil
}

func (p *audioPlayoutBuffer) reset(timestamp uint32, frame []float32) {
	p.pending = &audioPlayoutFrame{timestamp: timestamp, pcm: frame}
	p.prefill = nil
	p.prefillSamples = 0
	p.started = false
}

func alignAudioFrame(frame []float32, samples int) []float32 {
	// Real-time sinks advance while no packets arrive. Preserve a
	// decoder's natural multi-frame output, but do not replay an elapsed RTP gap
	// as queued silence when the next packet finally arrives.
	maxSamples := max(len(frame), FrameSamples)
	if samples > maxSamples {
		samples = maxSamples
	}
	if samples == len(frame) {
		return frame
	}
	aligned := make([]float32, samples)
	copy(aligned, frame)
	if samples > len(frame) {
		concealLostAudio(aligned[len(frame):], frame)
	}
	return aligned
}

// concealMaxSamples is how long a gap is bridged with repeated audio before it
// falls silent (40 ms at 16 kHz), so a long loss does not turn into a buzz.
const concealMaxSamples = 2 * SampleRate / 50

// concealLostAudio fills the hole a lost packet left after frame by repeating
// frame's last 10 ms with a falling volume, the way a decoder's packet-loss
// concealment bridges a short loss; past concealMaxSamples it stays silent.
func concealLostAudio(hole, frame []float32) {
	period := SampleRate / 100
	if len(frame) < period {
		return
	}
	tail := frame[len(frame)-period:]
	n := min(len(hole), concealMaxSamples)
	for i := 0; i < n; i++ {
		fade := 1 - float32(i)/float32(n)
		hole[i] = tail[i%period] * fade * 0.8
	}
}
