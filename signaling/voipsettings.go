package signaling

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

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

	// The settings WA's engine reads before it runs audio above 16 kHz
	// (configure_sampling_rates in the voip wasm): the override switch, and a
	// non-zero rate for the audio device, the conference bridge and the encoder.
	// Without all four it logs "disable swb due to invalid voip settings".
	SamplingRateOverrides  bool // options.enable_sampling_rate_overrides
	DriverSamplingRateMax  int  // audio_init.driver_sampling_rate_max
	ConfBridgeSamplingRate int  // conf_bridge.conf_bridge_sampling_rate
	EncodeSamplingRate     int  // encode.sampling_rate
	ForceSWB               bool // audio_dev.force_swb
	// Enable48kHzRTPClock mirrors options.enable_48khz_rtp_clock: audio RTP
	// timestamps count 48 kHz ticks instead of 16 kHz ones.
	Enable48kHzRTPClock bool
	// OpusMaxBandwidth mirrors encode.opus_max_bandwidth; 0 if absent.
	OpusMaxBandwidth int
	// DTX mirrors rc.dtx: the encoder stops sending in silence, as the shipped
	// clients do (an Android peer's timestamps jump while its sequence numbers
	// stay contiguous).
	DTX bool
	// DTXStartMs mirrors options.enable_additional_dtx_frames_at_call_start_ms:
	// how long after media starts silent frames are still sent.
	DTXStartMs int
	// MaxAudioResends mirrors options.max_rtp_audio_packet_resends: how often
	// one audio packet is sent again on NACKs; 0 if absent.
	MaxAudioResends int
	// The network health monitor's settings: options.enable_network_health_monitor
	// (NetHealthOff when present and off), net_health_ping_loss_sample_size,
	// net_health_max_sent_banner and net_health_max_sound_alert_audio; 0 if
	// absent.
	NetHealthOff        bool
	NetHealthSamples    int
	NetHealthMaxBanners int
	NetHealthMaxSounds  int
}

// MaxAudioRate is the highest audio sampling rate these settings let a call
// run: 48000 or 24000 when the server turned super-wideband on, else 16000.
// MLow stays at 16000 (its own super-wideband is a bandwidth-extension model
// meowcaller does not have).
func (vs *VoipSettings) MaxAudioRate() int {
	if vs == nil || !vs.Present || vs.UseMlowCodecV1 {
		return 16000
	}
	rate := 0
	if vs.SamplingRateOverrides && vs.DriverSamplingRateMax > 0 && vs.ConfBridgeSamplingRate > 0 && vs.EncodeSamplingRate > 0 {
		rate = min(vs.DriverSamplingRateMax, vs.ConfBridgeSamplingRate, vs.EncodeSamplingRate)
	}
	// ASSUMPTION: force_swb alone asks for 24 kHz; the wasm only shows it
	// forcing super-wideband on the audio device, not which rate.
	if vs.ForceSWB && rate < 24000 {
		rate = 24000
	}
	switch {
	case rate >= 48000:
		return 48000
	case rate >= 24000:
		return 24000
	}
	return 16000
}

// AudioRates lists the <audio enc=opus rate> values to advertise in a
// preaccept or accept: 16000 always, then each super-wideband rate up to max.
func AudioRates(max int) []string {
	rates := []string{"16000"}
	if max >= 24000 {
		rates = append(rates, "24000")
	}
	if max >= 48000 {
		rates = append(rates, "48000")
	}
	return rates
}

// audioRateBits maps an advertised rate to WA's capability bit for it (the
// engine's audio-caps parser: 8000=1, 16000=2, 24000=4, 48000=8).
var audioRateBits = map[string]uint8{"8000": 1, "16000": 2, "24000": 4, "48000": 8}

// AudioRateBits ORs the bits of the rates in an <audio> list.
func AudioRateBits(rates []string) uint8 {
	var bits uint8
	for _, r := range rates {
		bits |= audioRateBits[r]
	}
	return bits
}

// HighestCommonAudioRate is the rate a call runs at once both sides have
// advertised theirs: the highest rate in both sets, as WA's
// handle_peer_audio_caps ANDs its own caps with the peer's. 16000 when they
// share nothing better.
func HighestCommonAudioRate(own, peer uint8) int {
	common := own & peer
	switch {
	case common&8 != 0:
		return 48000
	case common&4 != 0:
		return 24000
	}
	return 16000
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
			UseMlowCodecV1   flexString `json:"use_mlow_codec_v1"`
			FrameMs          flexString `json:"frame_ms"`
			SamplingRate     flexString `json:"sampling_rate"`
			OpusMaxBandwidth flexString `json:"opus_max_bandwidth"`
		} `json:"encode"`
		RC struct {
			TargetBitrate flexString `json:"target_bitrate"`
			DTX           flexString `json:"dtx"`
		} `json:"rc"`
		Options struct {
			SamplingRateOverrides flexString  `json:"enable_sampling_rate_overrides"`
			Enable48kHzRTPClock   flexString  `json:"enable_48khz_rtp_clock"`
			DTXStartMs            flexString  `json:"enable_additional_dtx_frames_at_call_start_ms"`
			MaxAudioResends       flexString  `json:"max_rtp_audio_packet_resends"`
			NetHealthMonitor      *flexString `json:"enable_network_health_monitor"`
			NetHealthSamples      flexString  `json:"net_health_ping_loss_sample_size"`
			NetHealthMaxBanners   flexString  `json:"net_health_max_sent_banner"`
			NetHealthMaxSounds    flexString  `json:"net_health_max_sound_alert_audio"`
		} `json:"options"`
		AudioInit struct {
			DriverSamplingRateMax flexString `json:"driver_sampling_rate_max"`
		} `json:"audio_init"`
		ConfBridge struct {
			SamplingRate flexString `json:"conf_bridge_sampling_rate"`
		} `json:"conf_bridge"`
		AudioDev struct {
			ForceSWB flexString `json:"force_swb"`
		} `json:"audio_dev"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		lg.Warn().Int("bytes", len(raw)).Err(err).Msg("malformed voip_settings json")
		return nil, fmt.Errorf("signaling: parse voip_settings: %w", err)
	}
	vs := &VoipSettings{
		UseMlowCodecV1:         settingIsOn(string(doc.Encode.UseMlowCodecV1)),
		FrameMs:                atoiOrZero(string(doc.Encode.FrameMs)),
		TargetBitrate:          atoiOrZero(string(doc.RC.TargetBitrate)),
		Present:                true,
		SamplingRateOverrides:  settingIsOn(string(doc.Options.SamplingRateOverrides)),
		DriverSamplingRateMax:  atoiOrZero(string(doc.AudioInit.DriverSamplingRateMax)),
		ConfBridgeSamplingRate: atoiOrZero(string(doc.ConfBridge.SamplingRate)),
		EncodeSamplingRate:     atoiOrZero(string(doc.Encode.SamplingRate)),
		ForceSWB:               settingIsOn(string(doc.AudioDev.ForceSWB)),
		Enable48kHzRTPClock:    settingIsOn(string(doc.Options.Enable48kHzRTPClock)),
		OpusMaxBandwidth:       atoiOrZero(string(doc.Encode.OpusMaxBandwidth)),
		DTX:                    settingIsOn(string(doc.RC.DTX)),
		DTXStartMs:             atoiOrZero(string(doc.Options.DTXStartMs)),
		MaxAudioResends:        atoiOrZero(string(doc.Options.MaxAudioResends)),
		NetHealthOff:           doc.Options.NetHealthMonitor != nil && !settingIsOn(string(*doc.Options.NetHealthMonitor)),
		NetHealthSamples:       atoiOrZero(string(doc.Options.NetHealthSamples)),
		NetHealthMaxBanners:    atoiOrZero(string(doc.Options.NetHealthMaxBanners)),
		NetHealthMaxSounds:     atoiOrZero(string(doc.Options.NetHealthMaxSounds)),
	}
	lg.Debug().
		Bool("use_mlow_codec_v1", vs.UseMlowCodecV1).
		Int("frame_ms", vs.FrameMs).
		Int("target_bitrate", vs.TargetBitrate).
		Int("max_audio_rate", vs.MaxAudioRate()).
		Bool("rtp_clock_48khz", vs.Enable48kHzRTPClock).
		Bool("dtx", vs.DTX).
		Msg("parsed voip_settings")
	return vs, nil
}

// flexString takes a voip setting whether the server wrote it as a JSON
// string ("true", "60") or as a bare number or boolean.
type flexString string

// UnmarshalJSON keeps a string's contents or a scalar's literal text.
func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexString(s)
		return nil
	}
	*f = flexString(bytes.TrimSpace(b))
	return nil
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

// highQualityAudioKey matches the voip_settings keys WA's engine reads for
// audio above its usual 16 kHz wideband (from the WA Web voip wasm's settings
// table: encode.opus_max_bandwidth, force_swb, enable_48khz_rtp_clock,
// enable_sampling_rate_overrides, ...). None of them has been seen from the
// server so far: every call has run 16 kHz Opus or MLow.
var highQualityAudioKey = regexp.MustCompile(`(?i)swb|48khz|fullband|full_band|opus_max_bandwidth|sampling_rate`)

// HighQualityAudioSettings returns "section.key=value" for each setting in a
// voip_settings blob that would ask for audio above 16 kHz wideband, so a call
// can say so in its log the day the server turns one on.
func HighQualityAudioSettings(raw []byte) []string {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var found []string
	for section, body := range doc {
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) != nil {
			continue
		}
		for key, value := range fields {
			if highQualityAudioKey.MatchString(key) {
				found = append(found, section+"."+key+"="+strings.Trim(string(value), `"`))
			}
		}
	}
	sort.Strings(found)
	return found
}
