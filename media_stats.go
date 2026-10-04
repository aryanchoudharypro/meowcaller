package meowcaller

import (
	"sync"
	"sync/atomic"
	"time"
)

// MediaStats counts what happened to a call's media, for telling a silent
// call apart from one where nobody spoke. Counters only grow; sample twice
// and subtract for a rate. Readable after the call ends.
type MediaStats struct {
	// RTPReceived is inbound audio packets that authenticated and decoded.
	RTPReceived uint32
	// RTPPayloadTypeUnexpected is inbound packets of a payload type this call
	// doesn't carry.
	RTPPayloadTypeUnexpected uint32
	// SRTPUnprotectFailed is inbound audio packets that failed to
	// authenticate against any active participant.
	SRTPUnprotectFailed uint32
	// AudioFramesDecoded is inbound audio packets that decoded to coded audio.
	AudioFramesDecoded uint32
	// AudioFramesConcealed is frames replaced by silence because they could not
	// be read: an envelope that would not unwrap, or a body whose decode did
	// not end where the body said it should.
	AudioFramesConcealed uint32
	// MlowOffPointDropped is frames outside the decoder's operating point,
	// including standard Opus on the MLow payload type.
	MlowOffPointDropped uint32
	// MlowInactiveOrSID is frames that carried no coded voice (the peer saying
	// it is silent).
	MlowInactiveOrSID uint32
	// StandardOpusFrames is inbound packets that were standard Opus on the
	// payload type MLow shares (a peer outside the MLow rollout).
	StandardOpusFrames uint32
	// AudioFramesSent is outbound audio frames sent to the relay (silence
	// included).
	AudioFramesSent uint32
}

// AudioSilenceReason is the most specific explanation the counters support
// for a call carrying no audio.
type AudioSilenceReason string

const (
	AudioSilenceAuthenticationFailing AudioSilenceReason = "authentication_failing"
	AudioSilenceUnexpectedPayloadType AudioSilenceReason = "unexpected_payload_type"
	// AudioSilenceCodecRejectingFrames: packets authenticate, and the decoder
	// refuses or conceals them - the peer is sending something this decoder
	// does not read (a codec or operating-point mismatch).
	AudioSilenceCodecRejectingFrames AudioSilenceReason = "codec_rejecting_frames"
	AudioSilenceUnknown              AudioSilenceReason = "unknown"
)

// AudioHealth is one alarm from a call's audio watchdog.
type AudioHealth struct {
	// Stalled means no audio packets are arriving at all - a transport
	// problem. Otherwise packets arrive but none of them become sound.
	Stalled   bool
	SilentFor time.Duration
	// For a silent (not stalled) call: packets that arrived and frames
	// produced in the window that raised the alarm, and why.
	RTPReceived    uint32
	FramesProduced uint32
	Reason         AudioSilenceReason
}

type mediaStatsCounters struct {
	rtpReceived, payloadUnexpected, unprotectFailed, framesDecoded, framesSent atomic.Uint32
	framesConcealed, offPointDropped, inactiveOrSID, standardOpus              atomic.Uint32
}

func (s *mediaStatsCounters) snapshot() MediaStats {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/wacore/src/voip_control/media_stats.rs#L36-L55
	return MediaStats{
		RTPReceived:              s.rtpReceived.Load(),
		RTPPayloadTypeUnexpected: s.payloadUnexpected.Load(),
		SRTPUnprotectFailed:      s.unprotectFailed.Load(),
		AudioFramesDecoded:       s.framesDecoded.Load(),
		AudioFramesConcealed:     s.framesConcealed.Load(),
		MlowOffPointDropped:      s.offPointDropped.Load(),
		MlowInactiveOrSID:        s.inactiveOrSID.Load(),
		StandardOpusFrames:       s.standardOpus.Load(),
		AudioFramesSent:          s.framesSent.Load(),
	}
}

const (
	audioHealthTick         = 500 * time.Millisecond
	audioSilentWindow       = 2 * time.Second
	audioSilentMinPackets   = 12
	audioStallAfter         = 3 * time.Second
	audioHealthRealarmEvery = 10 * time.Second
)

// audioHealthWatch watches one call's audio for "connected but carrying
// nothing". Safe for concurrent use.
type audioHealthWatch struct {
	mu             sync.Mutex
	startedAt      time.Time
	windowStart    time.Time
	windowRTP      uint32
	windowProduced uint32
	totalArrivals  uint32
	lastArrival    time.Time
	windowStats    MediaStats
	silentSince    time.Time
	lastAlarmAt    time.Time
	stallReported  bool
}

func (w *audioHealthWatch) mediaStarted(now time.Time) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/wacore/src/voip_control/media_stats.rs#L158-L167
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.startedAt.IsZero() {
		return
	}
	w.startedAt = now
	w.windowStart = now
}

// onRTP records one inbound audio packet, whether or not it went on to
// authenticate or decode.
func (w *audioHealthWatch) onRTP(now time.Time) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/wacore/src/voip_control/media_stats.rs#L172-L179
	w.mu.Lock()
	w.windowRTP++
	w.totalArrivals++
	w.lastArrival = now
	w.stallReported = false
	w.mu.Unlock()
}

func (w *audioHealthWatch) onAudioProduced() {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/wacore/src/voip_control/media_stats.rs#L180-L182
	w.mu.Lock()
	w.windowProduced++
	w.mu.Unlock()
}

// poll evaluates the stall and silence rules; at most one alarm per call.
func (w *audioHealthWatch) poll(now time.Time, stats MediaStats) (AudioHealth, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/wacore/src/voip_control/media_stats.rs#L187-L267
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.startedAt.IsZero() {
		return AudioHealth{}, false
	}
	quietSince := w.lastArrival
	if quietSince.IsZero() {
		quietSince = w.startedAt
	}
	if elapsed := now.Sub(quietSince); !w.stallReported && elapsed >= audioStallAfter {
		w.stallReported = true
		return AudioHealth{Stalled: true, SilentFor: elapsed}, true
	}
	if w.totalArrivals == 0 || now.Sub(w.windowStart) < audioSilentWindow {
		return AudioHealth{}, false
	}
	rtp, produced := w.windowRTP, w.windowProduced
	windowBegan, openedAt := w.windowStart, w.windowStats
	w.windowStart, w.windowRTP, w.windowProduced, w.windowStats = now, 0, 0, stats
	if rtp < audioSilentMinPackets || produced > 0 {
		w.silentSince, w.lastAlarmAt = time.Time{}, time.Time{}
		return AudioHealth{}, false
	}
	if w.silentSince.IsZero() {
		w.silentSince = windowBegan
	}
	if !w.lastAlarmAt.IsZero() && now.Sub(w.lastAlarmAt) < audioHealthRealarmEvery {
		return AudioHealth{}, false
	}
	w.lastAlarmAt = now
	return AudioHealth{
		SilentFor:      now.Sub(w.silentSince),
		RTPReceived:    rtp,
		FramesProduced: produced,
		Reason:         dominantSilenceReason(statsDelta(stats, openedAt)),
	}, true
}

// statsDelta is what moved between the start of a window and its end.
func statsDelta(now, then MediaStats) MediaStats {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/wacore/src/voip_control/media_stats.rs#L276-L336
	sub := func(a, b uint32) uint32 {
		if a < b {
			return 0
		}
		return a - b
	}
	return MediaStats{
		RTPReceived:              sub(now.RTPReceived, then.RTPReceived),
		RTPPayloadTypeUnexpected: sub(now.RTPPayloadTypeUnexpected, then.RTPPayloadTypeUnexpected),
		SRTPUnprotectFailed:      sub(now.SRTPUnprotectFailed, then.SRTPUnprotectFailed),
		AudioFramesDecoded:       sub(now.AudioFramesDecoded, then.AudioFramesDecoded),
		AudioFramesConcealed:     sub(now.AudioFramesConcealed, then.AudioFramesConcealed),
		MlowOffPointDropped:      sub(now.MlowOffPointDropped, then.MlowOffPointDropped),
		MlowInactiveOrSID:        sub(now.MlowInactiveOrSID, then.MlowInactiveOrSID),
		StandardOpusFrames:       sub(now.StandardOpusFrames, then.StandardOpusFrames),
		AudioFramesSent:          sub(now.AudioFramesSent, then.AudioFramesSent),
	}
}

// dominantSilenceReason picks the most specific explanation a window's
// counters support. Dominance, not unanimity.
func dominantSilenceReason(delta MediaStats) AudioSilenceReason {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/wacore/src/voip_control/media_stats.rs#L342-L378
	switch {
	case delta.SRTPUnprotectFailed > delta.RTPReceived:
		return AudioSilenceAuthenticationFailing
	case delta.RTPPayloadTypeUnexpected > delta.RTPReceived:
		return AudioSilenceUnexpectedPayloadType
	case delta.MlowOffPointDropped > 0 || delta.AudioFramesConcealed > 0:
		return AudioSilenceCodecRejectingFrames
	}
	return AudioSilenceUnknown
}
