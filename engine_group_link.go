package meowcaller

import (
	"context"
	"time"

	"github.com/purpshell/meowcaller/signaling"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types/events"
)

// linkEpochFallbackDelay is how long the creator of a call-link call waits for a
// joiner's key epoch before distributing one itself.
const linkEpochFallbackDelay = 4 * time.Second

// groupKeyPending reports whether callID is a call-link call whose media runs
// ahead of its first key epoch, so outbound audio must stay gated.
func (e *engine) groupKeyPending(callID string) bool {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/05168a7f47bcaa78b6af8994c8c794d943991573/wacore/src/voip/engine.rs#L466-L468
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	return m != nil && m.group && m.link && !m.hasGroupEpoch
}

// maybeScheduleLinkEpochFallback arms a one-shot epoch fan-out for a call-link
// call this account created, once a remote device holds a PID in the roster
// and no epoch has arrived. The server does not always ask anyone to rekey
// when the first person joins a link call; without a key nobody hears anyone.
func (e *engine) maybeScheduleLinkEpochFallback(callID string) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/05168a7f47bcaa78b6af8994c8c794d943991573/src/voip/facade.rs#L1627-L1700
	self := e.c.wa.Store.GetLID()
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || !m.link || m.hasGroupEpoch || m.linkEpochFallbackSet || m.groupUpdate == nil ||
		m.creator.User != self.User || len(groupRekeyRecipients(*m.groupUpdate, self)) == 0 {
		e.mu.Unlock()
		return
	}
	m.linkEpochFallbackSet = true
	e.mu.Unlock()
	time.AfterFunc(linkEpochFallbackDelay, func() {
		e.mu.Lock()
		m := e.calls[callID]
		if m == nil || m.hasGroupEpoch || m.groupUpdate == nil {
			if m != nil {
				m.linkEpochFallbackSet = false
			}
			e.mu.Unlock()
			return
		}
		update := cloneGroupCallUpdate(*m.groupUpdate)
		m.linkEpochFallbackSet = false
		e.mu.Unlock()
		// ASSUMPTION: no joiner epoch arrived for this roster, so ours cannot
		// conflict with one at the same transaction. A late joiner epoch at the
		// same transaction would be rejected as a conflict.
		e.c.log.Info().
			Str("call_id", callID).
			Uint32("transaction_id", update.TransactionID).
			Msg("no key epoch from the call-link joiner; distributing one")
		if err := e.distributeRequestedGroupEpoch(context.Background(), update); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", callID).Msg("call-link epoch fallback failed")
		}
	})
}

// answerLinkRelayLatency answers a call-link joiner's relay latency probes.
func (e *engine) answerLinkRelayLatency(ev *events.CallRelayLatency, rl *waBinary.Node) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/05168a7f47bcaa78b6af8994c8c794d943991573/wacore/src/stanza/call.rs#L1052-L1068
	e.mu.Lock()
	m := e.calls[ev.CallID]
	offered := map[string]bool{}
	if m != nil && m.groupUpdate != nil && m.groupUpdate.Relay != nil {
		for _, endpoint := range m.groupUpdate.Relay.Endpoints {
			if endpoint.RelayName != "" {
				offered[endpoint.RelayName] = true
			}
		}
	}
	e.mu.Unlock()
	for _, resp := range linkRelayLatencyReplies(ev, rl, offered) {
		resp.Attrs["id"] = e.c.wa.GenerateMessageID()
		if err := e.c.wa.DangerousInternals().SendNode(context.Background(), resp); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", ev.CallID).Msg("answer call-link relay latency failed")
			return
		}
	}
	e.c.log.Debug().Str("call_id", ev.CallID).Str("to", ev.From.String()).
		Msg("answered call-link relay latency probe")
}

// linkRelayLatencyReplies builds one relaylatency reply per probed relay we
// were also offered, echoing the probe's latency and transaction-id.
func linkRelayLatencyReplies(ev *events.CallRelayLatency, rl *waBinary.Node, offered map[string]bool) []waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/05168a7f47bcaa78b6af8994c8c794d943991573/wacore/src/stanza/call.rs#L1052-L1068
	transactionID, hasTransaction := rl.Attrs["transaction-id"]
	var replies []waBinary.Node
	for i := range rl.GetChildren() {
		te := &rl.GetChildren()[i]
		if te.Tag != "te" {
			continue
		}
		ag := te.AttrGetter()
		name := ag.String("relay_name")
		if len(offered) > 0 && !offered[name] {
			continue
		}
		resp := signaling.BuildRelayLatency(&signaling.RelayLatencyParams{
			CallID:       ev.CallID,
			To:           ev.From,
			CallCreator:  ev.CallCreator,
			LatencyMs:    decodeLatency(ag.String("latency")),
			RelayName:    name,
			AddressBytes: nodeBytes(te),
		})
		if action, ok := resp.Content.([]waBinary.Node); hasTransaction && ok && len(action) == 1 {
			action[0].Attrs["transaction-id"] = transactionID
		}
		replies = append(replies, resp)
	}
	return replies
}
