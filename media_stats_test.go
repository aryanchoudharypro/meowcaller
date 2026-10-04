package meowcaller

import (
	"testing"
	"time"
)

func TestAudioHealthReportsAStallWhenNothingArrives(t *testing.T) {
	var w audioHealthWatch
	start := time.Unix(1000, 0)
	w.mediaStarted(start)
	if _, alarm := w.poll(start.Add(2900*time.Millisecond), MediaStats{}); alarm {
		t.Fatal("stalled before 3s")
	}
	health, alarm := w.poll(start.Add(3*time.Second), MediaStats{})
	if !alarm || !health.Stalled {
		t.Fatalf("no stall after 3s: %+v", health)
	}
	if _, alarm := w.poll(start.Add(4*time.Second), MediaStats{}); alarm {
		t.Fatal("a stall was reported twice")
	}
	// Reception recovers, then goes deaf mid-call: a new stall.
	w.onRTP(start.Add(5 * time.Second))
	if health, alarm := w.poll(start.Add(8*time.Second), MediaStats{}); !alarm || !health.Stalled {
		t.Fatalf("mid-call stall not reported: %+v", health)
	}
}

func TestAudioHealthNamesFailingAuthentication(t *testing.T) {
	var w audioHealthWatch
	start := time.Unix(1000, 0)
	w.mediaStarted(start)
	for i := range 20 {
		w.onRTP(start.Add(time.Duration(i) * 100 * time.Millisecond))
	}
	health, alarm := w.poll(start.Add(2*time.Second), MediaStats{SRTPUnprotectFailed: 20})
	if !alarm || health.Stalled || health.Reason != AudioSilenceAuthenticationFailing || health.RTPReceived != 20 {
		t.Fatalf("got %+v, alarm %v", health, alarm)
	}
	// Still silent in the next window, but inside the re-alarm interval.
	for i := range 20 {
		w.onRTP(start.Add(2*time.Second + time.Duration(i)*100*time.Millisecond))
	}
	if _, alarm := w.poll(start.Add(4*time.Second), MediaStats{SRTPUnprotectFailed: 40}); alarm {
		t.Fatal("re-alarmed before 10s")
	}
}

func TestAudioHealthIsQuietWhileAudioFlows(t *testing.T) {
	var w audioHealthWatch
	start := time.Unix(1000, 0)
	w.mediaStarted(start)
	for i := range 20 {
		w.onRTP(start.Add(time.Duration(i) * 100 * time.Millisecond))
		w.onAudioProduced()
	}
	if health, alarm := w.poll(start.Add(2*time.Second), MediaStats{RTPReceived: 20, AudioFramesDecoded: 20}); alarm {
		t.Fatalf("alarm on a healthy call: %+v", health)
	}
}

func TestSilenceReasonUsesDominance(t *testing.T) {
	cases := map[AudioSilenceReason]MediaStats{
		AudioSilenceAuthenticationFailing: {SRTPUnprotectFailed: 5, RTPReceived: 1},
		AudioSilenceUnexpectedPayloadType: {RTPPayloadTypeUnexpected: 5, RTPReceived: 1},
		AudioSilenceUnknown:               {SRTPUnprotectFailed: 1, RTPReceived: 5},
		AudioSilenceCodecRejectingFrames:  {MlowOffPointDropped: 20, RTPReceived: 20},
	}
	for want, delta := range cases {
		if got := dominantSilenceReason(delta); got != want {
			t.Errorf("%+v: got %s, want %s", delta, got, want)
		}
	}
	if d := statsDelta(MediaStats{RTPReceived: 3}, MediaStats{RTPReceived: 5}); d.RTPReceived != 0 {
		t.Fatal("delta went negative")
	}
}
