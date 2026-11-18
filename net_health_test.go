package meowcaller

import (
	"testing"
	"time"
)

// pingSecond sends one ping and answers it (or not) after rtt.
func pingSecond(m *netHealthMonitor, at time.Time, n int, answered bool, rtt time.Duration) {
	tx := [12]byte{byte(n), byte(n >> 8), 7}
	m.pingSent(tx, at)
	if answered {
		pong := make([]byte, 20)
		copy(pong[8:], tx[:])
		m.pong(pong, at.Add(rtt))
	}
}

func TestNetworkHealthTurnsPoorOnLostPingsOnceAndRecovers(t *testing.T) {
	m := newNetHealthMonitor(12, 0, 0)
	start := time.Unix(1000, 0)
	var changes []NetworkHealth
	for s := 0; s < 60; s++ {
		at := start.Add(time.Duration(s) * time.Second)
		bad := s >= 15 && s < 35
		pingSecond(m, at, s, !bad || s%2 == 0, 60*time.Millisecond)
		if h, ok := m.evaluate(at, 0, true); ok {
			changes = append(changes, h)
		}
	}
	if len(changes) < 2 {
		t.Fatalf("got %d changes, want poor then good: %+v", len(changes), changes)
	}
	if changes[0].Quality == NetworkGood || changes[0].PeerSide || !changes[0].SoundAlert {
		t.Fatalf("first change %+v, want our side poor with the sound", changes[0])
	}
	for _, h := range changes[1 : len(changes)-1] {
		if h.Quality == NetworkGood || h.SoundAlert {
			t.Fatalf("middle change %+v, want still poor, no sound", h)
		}
	}
	if last := changes[len(changes)-1]; last.Quality != NetworkGood || last.SoundAlert {
		t.Fatalf("last change %+v, want good without sound", last)
	}
}

func TestNetworkHealthBlamesThePeerForInboundLoss(t *testing.T) {
	m := newNetHealthMonitor(12, 0, 0)
	start := time.Unix(1000, 0)
	var got []NetworkHealth
	for s := 0; s < 30; s++ {
		at := start.Add(time.Duration(s) * time.Second)
		pingSecond(m, at, s, true, 50*time.Millisecond)
		if s >= 10 {
			m.observeInbound(80) // about 31% lost
		}
		if h, ok := m.evaluate(at, 0, true); ok {
			got = append(got, h)
		}
	}
	if len(got) == 0 || !got[0].PeerSide || got[0].Quality != NetworkPoor {
		t.Fatalf("got %+v, want the peer's connection poor", got)
	}
}

func TestNetworkHealthStopsAfterTheBannerLimit(t *testing.T) {
	m := newNetHealthMonitor(12, 2, 0)
	start := time.Unix(1000, 0)
	poorTurns, sounds := 0, 0
	reported := NetworkGood
	for s := 0; s < 400; s++ {
		at := start.Add(time.Duration(s) * time.Second)
		bad := (s/40)%2 == 1
		pingSecond(m, at, s, !bad, 40*time.Millisecond)
		if h, ok := m.evaluate(at, 0, true); ok {
			if reported == NetworkGood && h.Quality != NetworkGood {
				poorTurns++
			}
			if h.SoundAlert {
				sounds++
			}
			reported = h.Quality
		}
	}
	if poorTurns != 2 || sounds != 1 {
		t.Fatalf("poor turns %d sounds %d, want 2 and 1", poorTurns, sounds)
	}
}

func TestNetworkHealthIgnoresPingsNoRelayAnswers(t *testing.T) {
	m := newNetHealthMonitor(12, 0, 0)
	start := time.Unix(1000, 0)
	for s := 0; s < 30; s++ {
		at := start.Add(time.Duration(s) * time.Second)
		pingSecond(m, at, s, false, 0)
		if h, ok := m.evaluate(at, 0, true); ok {
			t.Fatalf("reported %+v with no pong ever seen", h)
		}
	}
}
