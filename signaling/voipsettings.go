package signaling

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/rs/zerolog"
)

// VoipSettings is the codec-relevant subset of the server's <voip_settings> JSON
// blob, delivered inline on an inbound <offer> and in the <ack> of an outbound
// offer. meowcaller reads it to choose the per-call audio codec.
//
// This is meowcaller-original glue: the whatsapp-rust reference does not parse
// voip_settings to pick a codec — it steers onto RFC Opus by advertising only
// <audio rate=8000> (see BuildAccept's audio_rates). Reading use_mlow_codec_v1 is
// a meowcaller-specific lever, so the parser carries no // Source of truth: port.
type VoipSettings struct {
	// UseMlowCodecV1 mirrors encode.use_mlow_codec_v1: true => Meta's 16 kHz MLow
	// codec, false => RFC Opus. Absent => false (Opus), as in WA's engine: its
	// voip-params parser leaves the use_mlow_codec field alone when the key is
	// missing, and nothing sets it on beforehand. Every call the user's logs hold
	// (Nov 2026) came without the key and the peer sent Opus each time.
	UseMlowCodecV1 bool
	// FrameMs mirrors encode.frame_ms (audio frame duration in ms); 0 if absent.
	FrameMs int
	// TargetBitrate mirrors rc.target_bitrate in bits/s; 0 if absent.
	TargetBitrate int
	// Present reports whether a non-empty voip_settings blob was parsed.
	Present bool
}

// ParseVoipSettings parses the <voip_settings> JSON content into the codec-relevant
// subset. An empty blob yields the zero VoipSettings (Opus); malformed JSON is an
// error. The values are stringly-typed on the wire ("true"/"60"), so each is
// converted explicitly; use_mlow_codec_v1 is on only for "true" or a non-zero
// number, the two forms WA's engine reads as on.
func ParseVoipSettings(raw []byte, log ...zerolog.Logger) (*VoipSettings, error) {
	lg := pickLog(log)
	if len(bytes.TrimSpace(raw)) == 0 {
		return &VoipSettings{}, nil
	}
	var doc struct {
		Encode struct {
			UseMlowCodecV1 string `json:"use_mlow_codec_v1"`
			FrameMs        string `json:"frame_ms"`
		} `json:"encode"`
		RC struct {
			TargetBitrate string `json:"target_bitrate"`
		} `json:"rc"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		lg.Warn().Int("bytes", len(raw)).Err(err).Msg("malformed voip_settings json")
		return nil, fmt.Errorf("signaling: parse voip_settings: %w", err)
	}
	vs := &VoipSettings{
		UseMlowCodecV1: settingIsOn(doc.Encode.UseMlowCodecV1),
		FrameMs:        atoiOrZero(doc.Encode.FrameMs),
		TargetBitrate:  atoiOrZero(doc.RC.TargetBitrate),
		Present:        true,
	}
	lg.Debug().
		Bool("use_mlow_codec_v1", vs.UseMlowCodecV1).
		Int("frame_ms", vs.FrameMs).
		Int("target_bitrate", vs.TargetBitrate).
		Msg("parsed voip_settings")
	return vs, nil
}

// settingIsOn reads a boolean voip setting the way WA's engine does: "true", or
// an integer other than zero.
func settingIsOn(s string) bool {
	if s == "true" {
		return true
	}
	n, err := strconv.Atoi(s)
	return err == nil && n != 0
}

// atoiOrZero parses a base-10 int, returning 0 for an absent or unparseable value.
func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
