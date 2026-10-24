package meowcaller

import (
	"context"
	"crypto/rand"
	"sync"
	"time"

	"github.com/purpshell/meowcaller/relay"
	"github.com/purpshell/meowcaller/signaling"
	"github.com/purpshell/meowcaller/stun"
)

// WA Web's VoIP defaults: tp->relay_unresponsive_timeout and
// mvp->media_rx_timeout_ms (5000 ms), tp->relay_data_timeout_ms (60000 ms),
// and its SCTP relay connection's same-path reconnect budget
// (WAWebVoipSctpConnectionManagerConstants).
const (
	relayUnresponsiveAfter = 5 * time.Second
	peerMediaTimeout       = 5 * time.Second
	reconnectGiveUpAfter   = 60 * time.Second
	reconnectWatchTick     = 500 * time.Millisecond
)

var relayRedialBackoff = [...]time.Duration{time.Second, 2 * time.Second, 3 * time.Second}

// reconnectStep is what one connectionWatch poll asks the media loop to do.
type reconnectStep struct {
	// changed reports a flip of reconnecting, the state the UI shows.
	changed      bool
	reconnecting bool
	redial       bool
	giveUp       bool
}

// connectionWatch decides when a call is reconnecting, when to redial its
// relays and when to give up on it. The relay answers our 1 Hz keepalive, so
// nothing from it for relayUnresponsiveAfter means our transport is gone; in
// a 1:1 call, no media from the peer for peerMediaTimeout means theirs is.
// Safe for concurrent use.
type connectionWatch struct {
	mu          sync.Mutex
	lastRelay   time.Time
	lastPeer    time.Time
	peerSeen    bool
	lostSince   time.Time
	shown       bool
	attempts    int
	redialing   bool
	nextAttempt time.Time
	lastDone    time.Time
}

func newConnectionWatch(now time.Time) *connectionWatch {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	return &connectionWatch{lastRelay: now}
}

func (w *connectionWatch) onRelayPacket(now time.Time) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	w.mu.Lock()
	w.lastRelay = now
	w.mu.Unlock()
}

func (w *connectionWatch) onPeerMedia(now time.Time) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	w.mu.Lock()
	w.lastPeer, w.peerSeen = now, true
	w.mu.Unlock()
}

// redialDone records the end of a redial round started by a poll.
func (w *connectionWatch) redialDone(now time.Time) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	w.mu.Lock()
	w.redialing = false
	w.lastDone = now
	if w.attempts < len(relayRedialBackoff) {
		w.nextAttempt = now.Add(relayRedialBackoff[w.attempts])
	}
	w.mu.Unlock()
}

// poll evaluates the watch at now. active is whether the call has connected
// (only then is reconnecting shown); watchPeer is whether the peer's media
// counts (1:1 calls whose peer isn't muted).
func (w *connectionWatch) poll(now time.Time, active, watchPeer bool) reconnectStep {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	w.mu.Lock()
	defer w.mu.Unlock()
	transportLost := now.Sub(w.lastRelay) >= relayUnresponsiveAfter
	peerLost := active && watchPeer && w.peerSeen && now.Sub(w.lastPeer) >= peerMediaTimeout
	var step reconnectStep
	if !transportLost && !peerLost {
		w.lostSince, w.attempts, w.nextAttempt = time.Time{}, 0, time.Time{}
		if w.shown {
			w.shown = false
			step.changed = true
		}
		return step
	}
	if w.lostSince.IsZero() {
		w.lostSince = now
		w.nextAttempt = now.Add(relayRedialBackoff[0])
	}
	if active && !w.shown {
		w.shown = true
		step.changed = true
	}
	step.reconnecting = w.shown
	if transportLost && !w.redialing && w.attempts < len(relayRedialBackoff) && !now.Before(w.nextAttempt) {
		w.attempts++
		w.redialing = true
		step.redial = true
	}
	exhausted := transportLost && w.attempts == len(relayRedialBackoff) && !w.redialing &&
		now.Sub(w.lastDone) >= relayUnresponsiveAfter
	if exhausted || now.Sub(w.lostSince) >= reconnectGiveUpAfter {
		step.giveUp = true
	}
	return step
}

// redialRelays reconnects every relay the fanout is bound to on the same
// path: a fresh UDP/DTLS/SCTP stack to the same endpoint, the same tokens and
// keys, then our allocate and a consent ping straight away so the relay
// bridges us again before the next keepalive.
func (e *engine) redialRelays(ctx context.Context, callID string, ch *relayFanout, rd *relayData, inbound bool, streamSsrcs [9]uint32, screen stun.WasmScreenShareStream) int {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	names, allocs := ch.snapshot()
	reconnected := 0
	for i, name := range names {
		ep, epRelay := e.boundEndpointByName(callID, rd, name, inbound)
		if ep == nil {
			e.c.log.Warn().Str("call_id", callID).Str("relay_name", name).Msg("no endpoint to redial")
			continue
		}
		var replacement *relay.RelayMediaChannel
		var allocate []byte
		var err error
		if allocs[i] != nil {
			replacement, allocate, err = e.connectOneRelay(ctx, epRelay, ep, streamSsrcs, screen)
		} else {
			// A group allocation is relay state the keepalive re-sends itself.
			replacement, err = e.dialRelay(ctx, ep)
		}
		if err != nil {
			e.c.log.Warn().Err(err).Str("call_id", callID).Str("relay_name", name).Msg("relay redial failed")
			continue
		}
		var tx [12]byte
		_, _ = rand.Read(tx[:])
		ping := stun.BuildWhatsappPing(tx, e.c.log)
		_, _ = replacement.Send(ping[:])
		if err := ch.replace(name, replacement, allocate); err != nil {
			_ = replacement.Close()
			e.c.log.Warn().Err(err).Str("call_id", callID).Str("relay_name", name).Msg("relay redial could not be installed")
			continue
		}
		reconnected++
		e.c.log.Info().Str("call_id", callID).Str("relay_name", name).Msg("relay redialed")
	}
	return reconnected
}

// boundEndpointByName finds the endpoint a fanout channel is bound to, in the
// call's current relay set: the group allocation's once the call has one,
// else the offer's.
func (e *engine) boundEndpointByName(callID string, rd *relayData, name string, inbound bool) (*relayEndpoint, *relayData) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	e.mu.Lock()
	if m := e.calls[callID]; m != nil && m.group && m.groupUpdate != nil {
		if groupRelay, err := groupRelayData(*m.groupUpdate, inbound); err == nil {
			rd = groupRelay
		}
	}
	e.mu.Unlock()
	for i := range rd.endpoints {
		if ep := &rd.endpoints[i]; ep.relayName == name && len(ep.addresses) != 0 {
			return ep, rd
		}
	}
	return nil, rd
}

// superviseConnection watches a running call's media and drives
// reconnecting: it reports the state on the Call, redials the relays when our
// transport goes quiet and ends the call when nothing comes back.
func (e *engine) superviseConnection(ctx context.Context, callID string, call *Call, watch *connectionWatch, redial func(context.Context) int, isGroup func() bool) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	ticker := time.NewTicker(reconnectWatchTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := time.Now()
		active := call != nil && call.State() == CallPhaseActive
		watchPeer := call != nil && !isGroup() && !call.peerMuted.Load()
		step := watch.poll(now, active, watchPeer)
		if step.changed && call != nil {
			e.c.log.Info().Str("call_id", callID).Bool("reconnecting", step.reconnecting).Msg("call reconnecting state changed")
			call.setReconnecting(step.reconnecting)
		}
		if step.giveUp {
			e.c.log.Warn().Str("call_id", callID).Msg("call connection lost; ending call")
			e.endLostCall(callID)
			return
		}
		if step.redial {
			go func() {
				n := redial(ctx)
				watch.redialDone(time.Now())
				e.c.log.Info().Str("call_id", callID).Int("relays_reconnected", n).Msg("relay redial round finished")
			}()
		}
	}
}

// endLostCall ends a call whose media never came back, telling the other side
// why with WhatsApp's media-timeout terminate reason.
func (e *engine) endLostCall(callID string) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	m := e.lookup(callID)
	if m == nil {
		return
	}
	e.mu.Lock()
	to, creator := m.from, m.creator
	e.mu.Unlock()
	reason := "media_rx_timeout"
	term := signaling.BuildTerminate(&signaling.TerminateParams{CallID: callID, To: to, CallCreator: creator, Reason: &reason})
	term.Attrs["id"] = e.nextCallNodeID()
	e.finishCall(callID, CallEndConnectionLost)
	if err := e.transmitCallNode(context.Background(), term); err != nil {
		e.c.log.Warn().Err(err).Str("call_id", callID).Str("reason", reason).Msg("send terminate failed")
	}
}

// CallEndConnectionLost is the OnEnd reason for a call ended because its
// media stopped and reconnecting failed.
const CallEndConnectionLost = "connection_lost"
