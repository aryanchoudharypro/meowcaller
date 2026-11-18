package meowcaller

import (
	"sync"
	"time"
)

// NetworkQuality is how well a call's network is carrying it.
type NetworkQuality int

const (
	NetworkGood NetworkQuality = iota
	// NetworkPoor is WhatsApp's "Poor connection" banner.
	NetworkPoor
	// NetworkVeryPoor is a connection barely carrying the call.
	NetworkVeryPoor
)

func (q NetworkQuality) String() string {
	switch q {
	case NetworkPoor:
		return "poor"
	case NetworkVeryPoor:
		return "very_poor"
	default:
		return "good"
	}
}

// NetworkHealth is a change in how well the call's network carries it, judged
// like WhatsApp's network health monitor: from the relay's answers to our
// once-a-second pings (lost pings and their round trip) and from the audio
// lost on the way in and out.
type NetworkHealth struct {
	Quality NetworkQuality
	// PeerSide is set when our own link is fine but the other person's audio
	// arrives with gaps: their connection is the poor one. 1:1 calls only.
	PeerSide bool
	// SoundAlert is set on the one change per call that WhatsApp plays its
	// alert for (net_health_max_sound_alert_audio).
	SoundAlert bool
	// What the verdict was drawn from.
	PingLossPercent         int
	RoundTrip               time.Duration
	InboundLossPercent      int
	PeerReportedLossPercent int
}

// OnNetworkHealth registers a callback fired when the call's network turns
// poor and when it recovers. At most a few poor turns per call are reported
// (net_health_max_sent_banner).
func (c *Call) OnNetworkHealth(fn func(NetworkHealth)) {
	c.mu.Lock()
	c.onNetworkHealth = fn
	c.mu.Unlock()
}

func (c *Call) onNetworkHealthFn() func(NetworkHealth) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onNetworkHealth
}

const (
	netHealthPings = 16
	// A ping not answered this long after it was sent counts as lost.
	netHealthPongWait = 1500 * time.Millisecond
	// Ticks a new verdict has to hold before it is reported: worse quickly,
	// better only once it stays better.
	netHealthWorseTicks  = 3
	netHealthBetterTicks = 6
	defaultPingSamples   = 12
	defaultMaxBanners    = 4
	defaultMaxSounds     = 1
)

// netHealthMonitor follows one call's network health. The keepalive loop
// feeds it pings and evaluates once a second; the receive loop feeds pongs.
type netHealthMonitor struct {
	mu      sync.Mutex
	pings   [netHealthPings]netHealthPing
	next    int
	inbound int // smoothed inbound audio loss, percent

	reported   NetworkQuality
	candidate  NetworkQuality
	candPeer   bool
	held       int
	banners    int
	sounds     int
	maxBanners int
	maxSounds  int
	samples    int
	// everAnswered: a relay has answered a ping in this call. Until one has,
	// unanswered pings say nothing (a relay that doesn't answer them at all
	// would otherwise read as a dead connection).
	everAnswered bool
}

type netHealthPing struct {
	tx       [12]byte
	sent     time.Time
	rtt      time.Duration
	answered bool
}

func newNetHealthMonitor(samples, maxBanners, maxSounds int) *netHealthMonitor {
	if samples <= 0 || samples > netHealthPings {
		samples = defaultPingSamples
	}
	if maxBanners <= 0 {
		maxBanners = defaultMaxBanners
	}
	if maxSounds <= 0 {
		maxSounds = defaultMaxSounds
	}
	return &netHealthMonitor{samples: samples, maxBanners: maxBanners, maxSounds: maxSounds}
}

func (m *netHealthMonitor) pingSent(tx [12]byte, now time.Time) {
	m.mu.Lock()
	m.pings[m.next] = netHealthPing{tx: tx, sent: now}
	m.next = (m.next + 1) % netHealthPings
	m.mu.Unlock()
}

// pong records the relay's answer to one of our pings: the transaction ID of
// a 0x0802 packet (bytes 8 to 20).
func (m *netHealthMonitor) pong(packet []byte, now time.Time) {
	if len(packet) < 20 {
		return
	}
	var tx [12]byte
	copy(tx[:], packet[8:20])
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.pings {
		p := &m.pings[i]
		if !p.answered && !p.sent.IsZero() && p.tx == tx {
			p.answered, p.rtt = true, now.Sub(p.sent)
			m.everAnswered = true
			return
		}
	}
}

// observeInbound folds one reception report's fraction lost (1/256ths of the
// peer's audio packets) into the inbound loss.
func (m *netHealthMonitor) observeInbound(fraction uint8) {
	now := (int(fraction)*100 + 255) / 256
	m.mu.Lock()
	m.inbound = (m.inbound + now + 1) / 2
	m.mu.Unlock()
}

// evaluate judges the last pings and losses and returns a change to report.
func (m *netHealthMonitor) evaluate(now time.Time, peerReportedLoss int, oneToOne bool) (NetworkHealth, bool) {
	// Source of truth: WA Web VoIP wasm rEjXwUYzMaj func 11678 ("network health status changed from %s to %s [PLR %.2f%%, RTT %dms%s]", ", Sound Alert") and options.net_health_* settings
	m.mu.Lock()
	defer m.mu.Unlock()
	var lost, answered int
	var rttSum time.Duration
	taken := 0
	for back := 1; back <= netHealthPings && taken < m.samples; back++ {
		p := m.pings[(m.next-back+netHealthPings)%netHealthPings]
		if p.sent.IsZero() || now.Sub(p.sent) < netHealthPongWait {
			continue
		}
		taken++
		if p.answered {
			answered++
			rttSum += p.rtt
		} else {
			lost++
		}
	}
	health := NetworkHealth{InboundLossPercent: m.inbound, PeerReportedLossPercent: peerReportedLoss}
	if taken < m.samples/2 {
		return health, false
	}
	if !m.everAnswered {
		taken, lost = 1, 0
	}
	health.PingLossPercent = lost * 100 / taken
	if answered > 0 {
		health.RoundTrip = rttSum / time.Duration(answered)
	}
	quality, peerSide := judgeNetwork(health, oneToOne)

	if quality != m.candidate || peerSide != m.candPeer {
		m.candidate, m.candPeer, m.held = quality, peerSide, 0
	}
	m.held++
	need := netHealthWorseTicks
	if quality < m.reported {
		need = netHealthBetterTicks
	}
	if quality == m.reported || m.held < need {
		return health, false
	}
	if quality != NetworkGood && m.reported == NetworkGood {
		if m.banners >= m.maxBanners {
			return health, false
		}
		m.banners++
		if m.sounds < m.maxSounds {
			m.sounds++
			health.SoundAlert = true
		}
	}
	m.reported = quality
	health.Quality, health.PeerSide = quality, peerSide
	return health, true
}

// judgeNetwork turns one second's measurements into a verdict. Our pings to
// the relay measure our own link; audio lost on the way in while they are
// fine points at the other person's.
func judgeNetwork(h NetworkHealth, oneToOne bool) (NetworkQuality, bool) {
	// ASSUMPTION: thresholds are ours; WA's packed net_health_max_ping_* values were not decoded.
	ours := NetworkGood
	switch {
	case h.PingLossPercent >= 50 || h.RoundTrip >= time.Second:
		ours = NetworkVeryPoor
	case h.PingLossPercent >= 20 || h.RoundTrip >= 500*time.Millisecond || h.PeerReportedLossPercent >= 20:
		ours = NetworkPoor
	}
	if ours != NetworkGood || !oneToOne {
		return ours, false
	}
	switch {
	case h.InboundLossPercent >= 35:
		return NetworkVeryPoor, true
	case h.InboundLossPercent >= 15:
		return NetworkPoor, true
	}
	return NetworkGood, false
}
