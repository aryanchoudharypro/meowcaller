<!-- Datasheet = three things only: the reference source VERBATIM, the Go envelope
     (signatures, no bodies), and implementation suggestions. No behavioral summary,
     no implementation. The verbatim source is the only authoritative content. -->

# Datasheet: `meowcaller/media-stats` counters and audio-health watchdog

Per-call media counters and the watchdog that tells a call carrying no audio apart
from one where nobody spoke.

**Validation vector:** `media_stats_test.go` — stall after 3s with nothing
arriving (and again after a mid-call recovery), silence with failing
authentication named by dominance, no alarm while audio flows, no re-alarm inside
10s, deltas saturate at zero.

**Reference pinned at:** `3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181`

## Reference source (verbatim — authoritative excerpt)

`wacore/src/voip_control/media_stats.rs` L1-L380:

```rust
//! Per-call media counters and the audio-health watchdog.
//!
//! Issue #1105 stayed open for months because every discard on the receive path was silent: a
//! payload type we did not expect, an SRTP tag that did not verify, a codec frame the decoder
//! refused, and a jitter buffer trimming its own head all returned without leaving a trace. A call
//! that carried no audio and a call where nobody spoke produced identical observations.
//!
//! The rule this module exists to enforce: **every `return` that discards a peer's audio increments
//! exactly one named counter.** The setup guards that precede it (no media plane, no PCM state, a
//! call that is not the one this packet belongs to) are deliberately not counted: they fire before
//! a packet is a packet, and counting them would bury the discards that mean something. The counters
//! are per call and die with it, which is why they are not in
//! [`crate::stats::SessionStats`] — that surface describes the WhatsApp session socket, and
//! `agent_docs/observability.md` keeps the two apart deliberately.
//!
//! Everything here is `Copy` and allocation-free. The engine is sans-io and drives one call on one
//! task, so plain `u32` beats atomics; increments saturate rather than wrap so a pathological peer
//! cannot make a counter run backwards.

/// Monotonic milliseconds. The shell supplies it; the engine never reads a clock.
pub type Millis = u64;

/// Sentinel deadline meaning "no timer pending"; the shell waits only on I/O until the next input.
pub const NEVER: Millis = u64::MAX;

/// Counters for one call's media plane, snapshot by value.
///
/// Read through `CallEngine::media_stats()` or `CallHandle::media_stats()`. Fields are additive for
/// the life of the call; a consumer that wants a rate samples twice and subtracts.
///
/// This is the neutral [`MediaStats`](super::MediaStats) under its historical engine name: one
/// definition, so the engine's counters and the seam's cannot drift. [`MediaStatsCell`] publishes it
/// across the task boundary.
pub use super::MediaStats as CallMediaStats;

/// A snapshot of [`CallMediaStats`] the drive loop publishes for a consumer to read.
///
/// The engine keeps plain `u32` because it is sans-io and single-threaded; this is the one place the
/// numbers cross a task boundary. The drive loop republishes whenever a counter moved, which on a
/// live call is most iterations, since `rtp_received` moves on every authenticated packet. That is
/// an uncontended lock at roughly the packet rate; measured, it does not appear in a profile.
#[derive(Debug, Default)]
pub struct MediaStatsCell(std::sync::Mutex<CallMediaStats>);

impl MediaStatsCell {
    /// Overwrite the published snapshot. Called by the drive loop; a poisoned lock is ignored,
    /// since a stale diagnostic must never take a live call down.
    pub fn publish(&self, stats: CallMediaStats) {
        if let Ok(mut slot) = self.0.lock() {
            *slot = stats;
        }
    }

    /// The most recent snapshot, or zeroes if the lock was poisoned or nothing has published yet.
    #[must_use]
    pub fn snapshot(&self) -> CallMediaStats {
        self.0.lock().map(|slot| *slot).unwrap_or_default()
    }
}

/// Why a call is carrying no audio, as far as the engine can tell.
///
/// This is the neutral [`MediaSilenceReason`](super::MediaSilenceReason) under its historical engine
/// name, one definition so the engine and a foreign backend name the same reasons.
pub use super::MediaSilenceReason as AudioSilenceReason;

#[cfg(feature = "voip")]
/// What the watchdog decided on one evaluation.
///
/// Kept separate from `CallEvent` so this module does not depend on the engine's event enum; the
/// engine maps it. That also keeps the watchdog unit-testable without building a call.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum AudioHealthAlarm {
    /// Audio RTP is arriving and none of it is becoming sound.
    Silent {
        silent_for_ms: Millis,
        rtp_received: u32,
        frames_produced: u32,
        dominant_reason: AudioSilenceReason,
    },
    /// No audio RTP has arrived for long enough that reception has stopped: either none ever
    /// arrived, or the peer's media stopped mid-call. A transport problem, not a codec one, and the
    /// two are split precisely because conflating them is how #1105 stayed open.
    Stalled { silent_for_ms: Millis },
}

#[cfg(feature = "voip")]
/// Evaluation cadence. Fine enough to catch the 2s window promptly, coarse enough that a call with
/// healthy audio pays one comparison per second.
const HEALTH_TICK_MS: Millis = 500;
#[cfg(feature = "voip")]
/// Sliding window over which "packets in, no audio out" is judged.
const SILENT_WINDOW_MS: Millis = 2_000;
#[cfg(feature = "voip")]
/// Packets that must land inside the window before silence is diagnosable; below this it is jitter,
/// not a diagnosis.
///
/// Sized by the SLOWEST cadence the receive path admits, not the common one. A 120 ms stream puts
/// only 16 or 17 packets in the window, so a minimum of 20 could never be met -- and because `poll`
/// rolls the window whenever the count falls short, such a call would stay silent forever without
/// ever producing an alarm. Twelve is 1.44 s of media at 120 ms and 0.72 s at 60 ms: still most of
/// the window in both, and still far above a jitter burst.
const SILENT_WINDOW_MIN_PACKETS: u32 = 12;
#[cfg(feature = "voip")]
/// No audio RTP at all for this long -- since media came up, or since the last packet -- is a
/// stalled reception.
const STALL_AFTER_MS: Millis = 3_000;
#[cfg(feature = "voip")]
/// Re-alarm cadence while the condition persists, so a truncated log still catches it.
const REALARM_MS: Millis = 10_000;

#[cfg(feature = "voip")]
/// Watches one call's audio for "connected but carrying nothing".
#[derive(Debug)]
pub(crate) struct AudioHealthWatch {
    /// When media came up. `NEVER` until then, which disables every rule below.
    started_at: Millis,
    /// Next evaluation deadline, folded into the engine's `poll_timeout`.
    deadline: Millis,
    window_start: Millis,
    window_rtp: u32,
    window_produced: u32,
    /// Arrivals since media came up, used only to tell "nothing arrived" from "nothing worked".
    total_arrivals: u32,
    /// When the last audio RTP packet arrived; `NEVER` until one does.
    ///
    /// The stall rule reads THIS rather than `total_arrivals`, because a call that goes deaf
    /// mid-way is the same failure as one that never heard anything and needs the same alarm.
    /// Keyed off "has anything ever arrived", one packet would disarm the rule for the rest of the
    /// call, and the window rule cannot cover the gap -- an empty window is below
    /// `SILENT_WINDOW_MIN_PACKETS`, so it resets without diagnosing anything.
    last_arrival: Millis,
    /// The counters as they stood when the current window opened.
    ///
    /// The alarm describes THIS window, so the reason has to come from what moved inside it. Read
    /// from the running totals instead, one concealed frame early in a call would keep naming the
    /// codec for every later silence, including one whose cause is somewhere else entirely.
    window_stats: CallMediaStats,
    /// Start of the current uninterrupted silence, for a monotonic `silent_for_ms`.
    silent_since: Option<Millis>,
    last_alarm_at: Option<Millis>,
    stall_reported: bool,
}

#[cfg(feature = "voip")]
impl Default for AudioHealthWatch {
    fn default() -> Self {
        Self {
            started_at: NEVER,
            deadline: NEVER,
            window_start: 0,
            window_rtp: 0,
            window_produced: 0,
            total_arrivals: 0,
            last_arrival: NEVER,
            window_stats: CallMediaStats::default(),
            silent_since: None,
            last_alarm_at: None,
            stall_reported: false,
        }
    }
}

#[cfg(feature = "voip")]
impl AudioHealthWatch {
    /// Arm the watchdog. Called when the relay accepts the allocate, which is the first moment
    /// inbound media is even possible.
    pub(crate) fn media_started(&mut self, now: Millis) {
        if self.started_at != NEVER {
            return;
        }
        self.started_at = now;
        self.window_start = now;
        self.deadline = now.saturating_add(HEALTH_TICK_MS);
    }

    /// The next evaluation deadline, or [`NEVER`] while disarmed.
    pub(crate) const fn deadline(&self) -> Millis {
        self.deadline
    }

    /// One audio RTP packet arrived, whether or not it went on to authenticate or decode.
    pub(crate) fn on_rtp(&mut self, now: Millis) {
        self.window_rtp = self.window_rtp.saturating_add(1);
        self.total_arrivals = self.total_arrivals.saturating_add(1);
        self.last_arrival = now;
        // Reception recovered, so a later stall is a new one and worth its own alarm.
        self.stall_reported = false;
    }

    pub(crate) fn on_audio_produced(&mut self) {
        self.window_produced = self.window_produced.saturating_add(1);
    }

    /// Evaluate the two rules. Returns at most one alarm per call of this function.
    ///
    /// `stats` is read, never written: the watchdog decides *that* a call is silent, and the
    /// counters explain *why*.
    pub(crate) fn poll(&mut self, now: Millis, stats: &CallMediaStats) -> Option<AudioHealthAlarm> {
        if self.started_at == NEVER || now < self.deadline {
            return None;
        }
        self.deadline = now.saturating_add(HEALTH_TICK_MS);

        // Nothing is ARRIVING: a transport problem, distinct from packets arriving that we cannot
        // turn into sound. `window_rtp` counts arrivals rather than authenticated packets, so a
        // call whose every packet fails its tag falls through to the silence rule below with a full
        // window, which is where it belongs -- `dominant_reason` then names the failing tags.
        //
        // Measured from the last packet, falling back to when media came up. A call that has never
        // heard anything and one that went deaf ten seconds in are the same failure to the person
        // holding the phone, and the window rule below can diagnose neither: an empty window is
        // under `SILENT_WINDOW_MIN_PACKETS`, so it resets rather than alarming.
        let quiet_since = if self.last_arrival == NEVER {
            self.started_at
        } else {
            self.last_arrival
        };
        let elapsed = now.saturating_sub(quiet_since);
        if !self.stall_reported && elapsed >= STALL_AFTER_MS {
            self.stall_reported = true;
            return Some(AudioHealthAlarm::Stalled {
                silent_for_ms: elapsed,
            });
        }
        if self.total_arrivals == 0 {
            return None;
        }

        if now.saturating_sub(self.window_start) < SILENT_WINDOW_MS {
            return None;
        }
        let (rtp, produced) = (self.window_rtp, self.window_produced);
        // Captured BEFORE the window rolls: the silence being reported started when this window
        // did, not now. Taking it after would make every first alarm claim zero milliseconds of
        // silence despite the two full seconds that authorised it.
        let window_began = self.window_start;
        let window_opened_at = self.window_stats;
        self.window_start = now;
        self.window_rtp = 0;
        self.window_produced = 0;
        self.window_stats = *stats;

        if produced > 0 || rtp < SILENT_WINDOW_MIN_PACKETS {
            // Audio is flowing, or too little arrived to judge. Either way the silence, if any,
            // is not established, so the run resets and `silent_for_ms` restarts on the next one.
            self.silent_since = None;
            self.last_alarm_at = None;
            return None;
        }

        let since = *self.silent_since.get_or_insert(window_began);
        let silent_for_ms = now.saturating_sub(since);
        let due = self
            .last_alarm_at
            .is_none_or(|at| now.saturating_sub(at) >= REALARM_MS);
        if !due {
            return None;
        }
        self.last_alarm_at = Some(now);
        Some(AudioHealthAlarm::Silent {
            silent_for_ms,
            rtp_received: rtp,
            frames_produced: produced,
            dominant_reason: dominant_reason(&window_delta(stats, &window_opened_at)),
        })
    }
}

#[cfg(feature = "voip")]
/// What moved between the start of a window and its end.
///
/// Every field is monotonic, so a saturating subtraction is the whole story. Taking the delta is
/// what keeps the alarm about the silence being reported rather than about anything that went wrong
/// earlier in the same call and has since recovered.
fn window_delta(now: &CallMediaStats, then: &CallMediaStats) -> CallMediaStats {
    CallMediaStats {
        rtp_received: now.rtp_received.saturating_sub(then.rtp_received),
        rtp_payload_type_unexpected: now
            .rtp_payload_type_unexpected
            .saturating_sub(then.rtp_payload_type_unexpected),
        srtp_unprotect_failed: now
            .srtp_unprotect_failed
            .saturating_sub(then.srtp_unprotect_failed),
        sframe_decrypt_failed: now
            .sframe_decrypt_failed
            .saturating_sub(then.sframe_decrypt_failed),
        audio_frames_decoded: now
            .audio_frames_decoded
            .saturating_sub(then.audio_frames_decoded),
        audio_frames_delivered: now
            .audio_frames_delivered
            .saturating_sub(then.audio_frames_delivered),
        audio_frames_concealed: now
            .audio_frames_concealed
            .saturating_sub(then.audio_frames_concealed),
        mlow_off_point_dropped: now
            .mlow_off_point_dropped
            .saturating_sub(then.mlow_off_point_dropped),
        mlow_inactive_or_sid: now
            .mlow_inactive_or_sid
            .saturating_sub(then.mlow_inactive_or_sid),
        foreign_frames_decoded: now
            .foreign_frames_decoded
            .saturating_sub(then.foreign_frames_decoded),
        audio_frames_without_decoder: now
            .audio_frames_without_decoder
            .saturating_sub(then.audio_frames_without_decoder),
        outbound_frames_without_encoder: now
            .outbound_frames_without_encoder
            .saturating_sub(then.outbound_frames_without_encoder),
        playout_trimmed_samples: now
            .playout_trimmed_samples
            .saturating_sub(then.playout_trimmed_samples),
        inbound_pipe_dropped: now
            .inbound_pipe_dropped
            .saturating_sub(then.inbound_pipe_dropped),
        audio_sink_dropped: now
            .audio_sink_dropped
            .saturating_sub(then.audio_sink_dropped),
        video_sink_dropped: now
            .video_sink_dropped
            .saturating_sub(then.video_sink_dropped),
        peer_keyframe_requests: now
            .peer_keyframe_requests
            .saturating_sub(then.peer_keyframe_requests),
        relay_packet_unclassified: now
            .relay_packet_unclassified
            .saturating_sub(then.relay_packet_unclassified),
        forwarding_envelope_rejected: now
            .forwarding_envelope_rejected
            .saturating_sub(then.forwarding_envelope_rejected),
        // NOT a delta: the flap limit is a property of the whole call, and a probe that has latched
        // stays latched. Resetting it per window would let a thrashing call look settled.
        codec_switches: now.codec_switches,
    }
}

#[cfg(feature = "voip")]
/// Pick the most specific explanation the counters support.
///
/// Order matters and is not by magnitude: a build with no decoder explains everything downstream of
/// it, and a failing tag explains a frame count of zero far better than "the codec refused it".
fn dominant_reason(stats: &CallMediaStats) -> AudioSilenceReason {
    // First because it explains everything downstream of it and is the only one a consumer can fix,
    // by building with a decoder for the codec the peer negotiated.
    if stats.audio_frames_without_decoder > 0 {
        return AudioSilenceReason::NoDecoderForNegotiatedCodec;
    }
    // Dominance, not unanimity, which is what this function is named for. Requiring every packet in
    // the window to fail meant one that authenticated -- and was then concealed like any other
    // undecodable frame -- handed the blame to the codec, for a window whose real problem is tags.
    // A minority of failed tags on an otherwise healthy call still does not rename the reason.
    if stats.srtp_unprotect_failed > stats.rtp_received {
        return AudioSilenceReason::AuthenticationFailing;
    }
    // SRTP authenticating and SFrame not is still an authentication failure, and the counter names
    // it exactly. It needs its own condition because the one above cannot fire for it: SRTP
    // succeeded, so `rtp_received` is not zero. Ahead of the codec reasons because the ciphertext is
    // handed to the codec and shows up there as concealment -- the symptom, reported by the layer
    // that did nothing wrong.
    if stats.sframe_decrypt_failed > 0 {
        return AudioSilenceReason::AuthenticationFailing;
    }
    // Dominance here too, for the reason the SRTP branch above gives: one packet that authenticated
    // and was then concealed must not hand a window of profile mismatches to the codec.
    if stats.rtp_payload_type_unexpected > stats.rtp_received {
        return AudioSilenceReason::UnexpectedPayloadType;
    }
    if stats.codec_switches >= CODEC_FLAP_LIMIT {
        return AudioSilenceReason::CodecFlapping;
    }
    if stats.mlow_off_point_dropped > 0 || stats.audio_frames_concealed > 0 {
        return AudioSilenceReason::CodecRejectingFrames;
    }
    AudioSilenceReason::Unknown
}

#[cfg(feature = "voip")]
/// Switches past this in one call mean the evidence is contradicting itself; the probe latches and
```

`wacore/src/voip_control/mod.rs` L239-L246 and L497-L548:

```rust
pub enum MediaSilenceReason {
    NoDecoderForNegotiatedCodec,
    AuthenticationFailing,
    UnexpectedPayloadType,
    CodecRejectingFrames,
    CodecFlapping,
    Unknown,
}

pub struct MediaStats {
    #[builder(default)]
    pub rtp_received: u32,
    #[builder(default)]
    pub rtp_payload_type_unexpected: u32,
    #[builder(default)]
    pub srtp_unprotect_failed: u32,
    #[builder(default)]
    pub sframe_decrypt_failed: u32,
    #[builder(default)]
    pub audio_frames_decoded: u32,
    #[builder(default)]
    pub audio_frames_delivered: u32,
    #[builder(default)]
    pub audio_frames_concealed: u32,
    #[builder(default)]
    pub mlow_off_point_dropped: u32,
    #[builder(default)]
    pub mlow_inactive_or_sid: u32,
    #[builder(default)]
    pub foreign_frames_decoded: u32,
    #[builder(default)]
    pub audio_frames_without_decoder: u32,
    #[builder(default)]
    pub outbound_frames_without_encoder: u32,
    #[builder(default)]
    pub playout_trimmed_samples: u32,
    #[builder(default)]
    pub inbound_pipe_dropped: u32,
    #[builder(default)]
    pub audio_sink_dropped: u32,
    #[builder(default)]
    pub video_sink_dropped: u32,
    #[builder(default)]
    pub peer_keyframe_requests: u32,
    #[builder(default)]
    pub relay_packet_unclassified: u32,
    #[builder(default)]
    pub forwarding_envelope_rejected: u32,
    #[builder(default)]
    pub codec_switches: u16,
}

impl MediaStats {
    /// Audio units that actually reached a consumer, whichever I/O mode is in use.
    #[must_use]
    pub const fn audio_produced(&self) -> u32 {
        self.audio_frames_decoded
            .saturating_add(self.audio_frames_delivered)
            .saturating_add(self.foreign_frames_decoded)
    }
}
```

## Go envelope

```go
type MediaStats struct { RTPReceived, RTPPayloadTypeUnexpected, SRTPUnprotectFailed, AudioFramesDecoded, AudioFramesSent uint32 }
type AudioSilenceReason string
type AudioHealth struct { Stalled bool; SilentFor time.Duration; RTPReceived, FramesProduced uint32; Reason AudioSilenceReason }

func (c *Call) MediaStats() MediaStats
func (c *Call) OnAudioHealth(fn func(AudioHealth))
```

## Implementation suggestions

- meowcaller's `DecodeAudio` authenticates and decodes in one step, so its failure
  counts as `SRTPUnprotectFailed`; the reference's codec-level counters (concealment,
  MLow off-point, codec switches, no decoder) have no separate signal yet and are not
  ported, so `dominantSilenceReason` covers authentication, payload type and unknown.
- The watchdog is polled on a 500ms ticker in the media loop instead of the
  reference's engine deadline; the rules and constants are unchanged.
