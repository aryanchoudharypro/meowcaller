package meowcaller

import (
	"context"
	"time"
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
