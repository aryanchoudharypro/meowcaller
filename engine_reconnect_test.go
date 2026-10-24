package meowcaller

import (
	"testing"
	"time"
)

func TestConnectionWatchStaysQuietWhileTheRelayAnswers(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	w := newConnectionWatch(start)
	w.onPeerMedia(start)
	for s := 1; s <= 30; s++ {
		now := start.Add(time.Duration(s) * time.Second)
		w.onRelayPacket(now)
		w.onPeerMedia(now)
		if step := w.poll(now, true, true); step != (reconnectStep{}) {
			t.Fatalf("at %ds: %+v, want nothing", s, step)
		}
	}
}

func TestConnectionWatchRedialsThreeTimesThenGivesUp(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	w := newConnectionWatch(start)
	at := func(d time.Duration) time.Time { return start.Add(d) }

	if step := w.poll(at(4*time.Second), true, true); step.changed || step.redial {
		t.Fatalf("4s of quiet already acted: %+v", step)
	}
	step := w.poll(at(5*time.Second), true, true)
	if !step.changed || !step.reconnecting || step.redial || step.giveUp {
		t.Fatalf("at 5s: %+v, want reconnecting shown and the first redial a second later", step)
	}
	// Each round starts its backoff (1s, 2s, 3s) after the previous one ends.
	done := 6 * time.Second
	for round, backoff := range []time.Duration{0, 2 * time.Second, 3 * time.Second} {
		if round > 0 {
			if step := w.poll(at(done+backoff-time.Millisecond), true, true); step.redial {
				t.Fatalf("round %d redialed before its %s backoff", round+1, backoff)
			}
		}
		step := w.poll(at(done+backoff), true, true)
		if !step.redial || step.giveUp {
			t.Fatalf("round %d: %+v, want a redial", round+1, step)
		}
		if step := w.poll(at(done+backoff+time.Second), true, true); step.redial || step.giveUp {
			t.Fatalf("round %d redialed again or gave up mid-redial: %+v", round+1, step)
		}
		done += backoff + 10*time.Second
		w.redialDone(at(done))
	}
	if step := w.poll(at(done+4*time.Second), true, true); step.giveUp {
		t.Fatal("gave up before the last redial had its chance")
	}
	if step := w.poll(at(done+5*time.Second), true, true); !step.giveUp || step.redial {
		t.Fatalf("after the last redial: %+v, want giving up", step)
	}
}

func TestConnectionWatchClearsWhenTheRelayIsBack(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	w := newConnectionWatch(start)
	w.poll(start.Add(5*time.Second), true, false)
	w.poll(start.Add(6*time.Second), true, false)
	w.redialDone(start.Add(7 * time.Second))
	w.onRelayPacket(start.Add(7 * time.Second))
	step := w.poll(start.Add(7500*time.Millisecond), true, false)
	if !step.changed || step.reconnecting {
		t.Fatalf("%+v, want reconnecting cleared", step)
	}
	// A later drop starts over with all three redials.
	if step := w.poll(start.Add(13*time.Second), true, false); !step.changed || !step.reconnecting {
		t.Fatalf("second drop: %+v", step)
	}
	if step := w.poll(start.Add(14*time.Second), true, false); !step.redial {
		t.Fatalf("second drop did not redial: %+v", step)
	}
}

func TestConnectionWatchPeerSilenceShowsReconnectingAndTimesOut(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	w := newConnectionWatch(start)
	w.onPeerMedia(start)
	relayAlive := func(d time.Duration) time.Time {
		now := start.Add(d)
		w.onRelayPacket(now)
		return now
	}
	if step := w.poll(relayAlive(5*time.Second), true, false); step.changed {
		t.Fatal("peer silence counted although the peer isn't watched (muted or a group call)")
	}
	step := w.poll(relayAlive(5*time.Second), true, true)
	if !step.changed || !step.reconnecting || step.redial {
		t.Fatalf("%+v, want reconnecting without redialing a relay that answers", step)
	}
	if step := w.poll(relayAlive(64*time.Second), true, true); step.giveUp {
		t.Fatal("gave up before a minute of reconnecting")
	}
	if step := w.poll(relayAlive(65*time.Second), true, true); !step.giveUp {
		t.Fatal("still reconnecting after a minute without the peer's media")
	}
}

func TestConnectionWatchDoesNotShowReconnectingBeforeTheCallConnects(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	w := newConnectionWatch(start)
	if step := w.poll(start.Add(6*time.Second), false, true); step.changed || step.reconnecting || step.redial {
		t.Fatalf("%+v, want nothing on the first quiet poll", step)
	}
	step := w.poll(start.Add(7*time.Second), false, true)
	if step.changed || step.reconnecting || !step.redial {
		t.Fatalf("%+v, want a silent redial while connecting", step)
	}
}
