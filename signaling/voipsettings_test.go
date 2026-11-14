package signaling

import (
	"os"
	"strings"
	"testing"
)

// TestParseVoipSettings is the KAT for the voip_settings parser, wired to the
// captured sample blob (use_mlow_codec_v1="true", frame_ms="60",
// target_bitrate="24000"). Skipped until ParseVoipSettings lands.
func TestParseVoipSettings(t *testing.T) {
	raw, err := os.ReadFile("testdata/voip_settings_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	vs, err := ParseVoipSettings(raw)
	if err != nil {
		t.Fatalf("ParseVoipSettings: %v", err)
	}
	if !vs.Present {
		t.Error("Present = false, want true")
	}
	if !vs.UseMlowCodecV1 {
		t.Error("UseMlowCodecV1 = false, want true (sample sets it true)")
	}
	if vs.FrameMs != 60 {
		t.Errorf("FrameMs = %d, want 60", vs.FrameMs)
	}
	if vs.TargetBitrate != 24000 {
		t.Errorf("TargetBitrate = %d, want 24000", vs.TargetBitrate)
	}
}

// TestParseVoipSettingsOpus pins the codec lever: use_mlow_codec_v1="false" must
// parse to UseMlowCodecV1=false. Skipped until ParseVoipSettings lands.
func TestParseVoipSettingsOpus(t *testing.T) {
	vs, err := ParseVoipSettings([]byte(`{"encode":{"use_mlow_codec_v1":"false","frame_ms":"60"}}`))
	if err != nil {
		t.Fatalf("ParseVoipSettings: %v", err)
	}
	if vs.UseMlowCodecV1 {
		t.Error("UseMlowCodecV1 = true, want false")
	}
}

// Today's settings (keys as the server sends them) ask for nothing above
// wideband; the keys WA's engine reads for more are reported when present.
func TestHighQualityAudioSettingsAreReported(t *testing.T) {
	today := []byte(`{"encode":{"complexity":"5","frame_ms":"60","max_frames_per_packet":"3"},` +
		`"options":{"relay_ping_sample_rate":"3","network_optimizer_random_probe_sample_rate":"0","neteq_enable_codec_plc":"true"},` +
		`"bwe":{"sbwe_max_target_bitrate":"100000"}}`)
	if got := HighQualityAudioSettings(today); len(got) != 0 {
		t.Fatalf("today's settings reported as high quality: %v", got)
	}
	someday := []byte(`{"encode":{"opus_max_bandwidth":"4","force_swb":"1"},"options":{"enable_48khz_rtp_clock":"true"}}`)
	got := HighQualityAudioSettings(someday)
	want := []string{"encode.force_swb=1", "encode.opus_max_bandwidth=4", "options.enable_48khz_rtp_clock=true"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}
