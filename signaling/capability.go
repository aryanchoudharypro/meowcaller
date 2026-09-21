package signaling

import "bytes"

// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/main/wacore/src/stanza/call.rs
// (capability_bit, mlow_after_peer_capability, without_mlow_capability)

// capabilityVersion is the version a <capability> blob must declare for its
// bitmask to be readable.
const capabilityVersion = 1

// capabilityHeaderLen is the [version][len] prefix before the bitmask.
const capabilityHeaderLen = 2

// CapabilityMlowCodecV1 is the capability index for use_mlow_codec_v1: the peer
// gate that decides whether a call runs MLow or standard Opus.
const CapabilityMlowCodecV1 = 31

// CapabilityBit is what a peer's <capability> says about one index.
//
// Three states, not a *bool, because the official client treats two of them in
// OPPOSITE directions: a peer that sends no blob at all is skipped and nothing
// resets, so the feature stays on; a peer whose blob is unreadable falls back to
// a capability against which every query answers false, so everything resets.
// Getting those two the same way round is the bug this type exists to prevent.
type CapabilityBit int

const (
	// CapabilityUnknown means the peer announced nothing. Not evidence either way.
	CapabilityUnknown CapabilityBit = iota
	// CapabilitySet means the peer announced the index.
	CapabilitySet
	// CapabilityClear means the peer announced a valid blob without the index, or
	// a blob we cannot read, which the official client treats the same way.
	CapabilityClear
)

// CapabilityBitAt reads one index out of a peer <capability> blob. version is the
// node's "ver" attribute (hasVersion false when it was missing or unparseable)
// and blob its content.
//
// An unparseable ver is not the same as a missing one: the client rebuilds the
// capability with a version that fails every query, so it reads as Clear. An
// empty blob is Clear too, not Unknown — the client's fallback fires on
// blob == null || ver <= 0 || len <= 0. Only the absence of the node itself is
// Unknown, which is the caller's business to signal.
func CapabilityBitAt(version uint32, hasVersion bool, blob []byte, index uint32) CapabilityBit {
	if !hasVersion || version < capabilityVersion {
		return CapabilityClear
	}
	// The blob's OWN version, which is not the attribute's. Skipping this byte
	// lets [0, 5, ...] answer Set for index 31 — MLow kept enabled against a peer
	// that had fallen back to native Opus, which is then sent MLow and hears
	// silence. That is the exact shape of whatsapp-rust#1105.
	if len(blob) < capabilityHeaderLen {
		return CapabilityClear
	}
	if uint32(blob[0]) != capabilityVersion {
		return CapabilityClear
	}
	declaredLen := int(blob[1])
	mask := blob[capabilityHeaderLen:]
	// A blob promising more mask bytes than it carries is truncated, and a
	// truncated blob is one the peer never wrote. The client's parser fails the
	// whole blob rather than reading the prefix that did arrive.
	if declaredLen > len(mask) {
		return CapabilityClear
	}
	byteIdx := int(index / 8)
	// The reference masks the byte index with 31 before indexing, so an index past
	// 255 aliases onto a low one rather than reading out of range. Mirrored so a
	// blob is read the way the peer that built it reads it.
	byteIdx &= 31
	if byteIdx >= declaredLen {
		return CapabilityClear
	}
	if mask[byteIdx]&(1<<(index%8)) != 0 {
		return CapabilitySet
	}
	return CapabilityClear
}

// MlowAfterPeerCapability applies one peer's capability to the locally-enabled
// MLow setting. The official client runs set_voip_params_from_capability and then
// reset_voip_params_if_no_capability, and the second walks every participant: if
// ANY of them fails to announce the index, the parameter drops to its safe
// default. So the effective value is `local && every peer announced it`, and it
// is one boolean driving both the encoder and the receive-side decoder
// registration — not a pair of per-direction flags.
//
// CapabilityUnknown deliberately does not reset: a peer that sent no blob is
// skipped by the client rather than treated as a refusal.
func MlowAfterPeerCapability(local bool, peer CapabilityBit) bool {
	return local && peer != CapabilityClear
}

// WithoutMlowCapability clears index 31 from a capability blob, selecting
// WhatsApp's standard Opus fallback instead of MLow. Index 31 lives in byte
// 31/8 == 3 of the bitmask, which starts at index 2, so it is blob[5], and the
// bit within it is 1 << (31 % 8) == 0x80.
func WithoutMlowCapability(capability []byte) []byte {
	out := bytes.Clone(capability)
	if len(out) > 5 {
		out[5] &= 0x7f
	}
	return out
}

// CapabilityStandardOpusOffer is the <offer>/<accept> capability selecting
// WhatsApp's standard Opus fallback instead of MLow.
var CapabilityStandardOpusOffer = WithoutMlowCapability(CapabilityOffer)

// CapabilityStandardOpusPreaccept is the <preaccept> capability selecting
// standard Opus instead of MLow.
var CapabilityStandardOpusPreaccept = WithoutMlowCapability(CapabilityPreaccept)

// CapabilityStandardOpusVideoOffer is the video <offer> capability selecting
// standard Opus for its audio stream instead of MLow.
var CapabilityStandardOpusVideoOffer = WithoutMlowCapability(CapabilityVideoOffer)

// standardOpusPT120Settings is the directional decoder selection sent in a
// standard-Opus <accept>: it tells the peer to decode what we will actually send
// instead of leaving it on its MLow decode path. Android overlays these fields on
// its local defaults, so echoing the peer's large rollout settings back is
// unnecessary and can be harmful — send only the two that select the codec.
// Source of truth: https://github.com/oxidezap/whatsapp-rust/pull/1050
var standardOpusPT120Settings = []byte(`{"encode":{"use_mlow_codec_v1":"false"},"options":{"enable_48khz_rtp_clock":"false"}}`)

// standardOpusPT111Settings is standardOpusPT120Settings with the 48 kHz RTP
// clock variant selected.
var standardOpusPT111Settings = []byte(`{"encode":{"use_mlow_codec_v1":"false"},"options":{"enable_48khz_rtp_clock":"true"}}`)

// StandardOpusVoipSettings returns the <voip_settings> payload for a
// standard-Opus answer. An MLow answer sends none: it is the default the peer
// already assumes.
func StandardOpusVoipSettings(enable48kHzRTPClock bool) []byte {
	if enable48kHzRTPClock {
		return bytes.Clone(standardOpusPT111Settings)
	}
	return bytes.Clone(standardOpusPT120Settings)
}
