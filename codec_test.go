package meowcaller

import (
	"testing"

	"github.com/purpshell/meowcaller/signaling"
)

// TestSelectAudioCodec pins the codec selection to WA's engine: MLow only when
// the call's voip_settings turn use_mlow_codec_v1 on; no settings, or settings
// without the key, are standard Opus.
func TestSelectAudioCodec(t *testing.T) {
	cases := []struct {
		name string
		vs   *signaling.VoipSettings
		want AudioCodec
	}{
		{"nil is opus", nil, AudioCodecOpus},
		{"absent is opus", &signaling.VoipSettings{Present: false, UseMlowCodecV1: false}, AudioCodecOpus},
		{"mlow true", &signaling.VoipSettings{Present: true, UseMlowCodecV1: true}, AudioCodecMlow},
		{"mlow false -> opus", &signaling.VoipSettings{Present: true, UseMlowCodecV1: false}, AudioCodecOpus},
	}
	for _, c := range cases {
		if got := selectAudioCodec(c.vs); got != c.want {
			t.Errorf("%s: selectAudioCodec = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestVoipSettingsWithoutTheMlowKeyAreOpus(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"encode":{"frame_ms":"60","max_frames_per_packet":"3"}}`: false,
		`{"encode":{"use_mlow_codec_v1":"true"}}`:                   true,
		`{"encode":{"use_mlow_codec_v1":"1"}}`:                      true,
		`{"encode":{"use_mlow_codec_v1":"0"}}`:                      false,
		`{"encode":{"use_mlow_codec_v1":"false"}}`:                  false,
		``: false,
	} {
		vs, err := signaling.ParseVoipSettings([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if vs.UseMlowCodecV1 != want {
			t.Errorf("%q: use_mlow_codec_v1 = %v, want %v", raw, vs.UseMlowCodecV1, want)
		}
	}
}
