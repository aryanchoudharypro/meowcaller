package signaling

import (
	"reflect"
	"testing"
)

func TestSuperWidebandNeedsAllOfWAsSettings(t *testing.T) {
	cases := []struct {
		json string
		want int
	}{
		{`{"encode":{"use_mlow_codec_v1":"false"}}`, 16000},
		{`{"options":{"enable_sampling_rate_overrides":"1"},"audio_init":{"driver_sampling_rate_max":"48000"},"conf_bridge":{"conf_bridge_sampling_rate":"48000"},"encode":{"sampling_rate":"48000"}}`, 48000},
		{`{"options":{"enable_sampling_rate_overrides":true},"audio_init":{"driver_sampling_rate_max":48000},"conf_bridge":{"conf_bridge_sampling_rate":"24000"},"encode":{"sampling_rate":"48000"}}`, 24000},
		// No encoder rate: WA's "disable swb due to invalid voip settings".
		{`{"options":{"enable_sampling_rate_overrides":"1"},"audio_init":{"driver_sampling_rate_max":"48000"},"conf_bridge":{"conf_bridge_sampling_rate":"48000"}}`, 16000},
		{`{"audio_dev":{"force_swb":"1"}}`, 24000},
		// MLow stays wideband.
		{`{"encode":{"use_mlow_codec_v1":"true","sampling_rate":"48000"},"options":{"enable_sampling_rate_overrides":"1"},"audio_init":{"driver_sampling_rate_max":"48000"},"conf_bridge":{"conf_bridge_sampling_rate":"48000"}}`, 16000},
	}
	for _, c := range cases {
		vs, err := ParseVoipSettings([]byte(c.json))
		if err != nil {
			t.Fatal(err)
		}
		if got := vs.MaxAudioRate(); got != c.want {
			t.Errorf("%s: got %d, want %d", c.json, got, c.want)
		}
	}
}

func TestAudioRateNegotiation(t *testing.T) {
	if got := AudioRates(16000); !reflect.DeepEqual(got, []string{"16000"}) {
		t.Fatal(got)
	}
	if got := AudioRates(48000); !reflect.DeepEqual(got, []string{"16000", "24000", "48000"}) {
		t.Fatal(got)
	}
	own := AudioRateBits(AudioRates(48000))
	if got := HighestCommonAudioRate(own, AudioRateBits([]string{"16000", "8000"})); got != 16000 {
		t.Fatal(got)
	}
	if got := HighestCommonAudioRate(own, AudioRateBits([]string{"16000", "24000"})); got != 24000 {
		t.Fatal(got)
	}
	if got := HighestCommonAudioRate(AudioRateBits(AudioRates(16000)), AudioRateBits([]string{"48000", "16000"})); got != 16000 {
		t.Fatal(got)
	}
}
