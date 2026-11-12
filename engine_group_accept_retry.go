package meowcaller

// maxGroupAcceptRetries bounds how often a committed roster may send the
// <accept> again, so a server that keeps listing us as not connected cannot
// loop it.
const maxGroupAcceptRetries = 3

// retryGroupAccept sends the group <accept> again when a committed roster still
// does not list us as connected after we answered. A 1:1 call that is upgraded to a
// group while it rings loses an <accept> that was sent to the single caller before
// the upgrade, and the roster that follows shows us still invited.
func (e *engine) retryGroupAccept(callID string, roster groupCallUpdate) {
	// Source of truth: WhatsApp Web VoIP wasm post_process_group_info, logging "self is not connected or terminated, retry accept", enabled by vp->enable_retry_accept_on_group_upgrade_race
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || !m.group || !m.answered || m.direction != CallDirectionIncoming ||
		m.acceptRetries >= maxGroupAcceptRetries || selfConnected(roster, m.selfLID) {
		e.mu.Unlock()
		return
	}
	if m.call != nil && m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return
	}
	m.acceptRetries++
	attempt := m.acceptRetries
	creator := m.creator
	e.mu.Unlock()

	e.c.log.Debug().
		Str("call_id", callID).
		Uint32("transaction_id", roster.TransactionID).
		Int("attempt", attempt).
		Msg("roster does not list us as connected after answering; sending accept again")
	if err := e.sendGroupAccept(callID, creator); err != nil {
		e.c.log.Warn().Err(err).Str("call_id", callID).Msg("send accept again failed")
	}
}
