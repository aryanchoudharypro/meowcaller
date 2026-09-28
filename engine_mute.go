package meowcaller

import (
	"context"
	"errors"
	"fmt"

	"github.com/purpshell/meowcaller/signaling"
	"go.mau.fi/whatsmeow/types"
)

func muteStateAttr(muted bool) string {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/wacore/src/stanza/call.rs#L1165-L1180
	if muted {
		return "1"
	}
	return "0"
}

// muteTarget is where this call's mute state is addressed, or ok=false while
// there is no answered call to carry it.
func (e *engine) muteTarget(callID string) (to, creator types.JID, ok bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/src/voip/facade.rs#L3916-L3931
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil {
		e.mu.Unlock()
		return types.EmptyJID, types.EmptyJID, false
	}
	to, creator = m.from, m.creator
	group, direction, answered, call := m.group, m.direction, m.acceptPending || m.acceptSent, m.call
	e.mu.Unlock()
	if to.IsEmpty() {
		return to, creator, false
	}
	switch {
	case group:
		return to, creator, true
	case direction == CallDirectionIncoming:
		return to, creator, answered
	default:
		return to, creator, call != nil && call.isPeerAccepted()
	}
}

// setMuted applies and announces this client's microphone state.
func (e *engine) setMuted(c *Call, muted bool) error {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/src/voip/facade.rs#L3876-L3913
	if c.State() == CallPhaseEnded {
		return errors.New("meowcaller: call is not active")
	}
	c.muteMu.Lock()
	defer c.muteMu.Unlock()
	to, creator, ok := e.muteTarget(c.id)
	if !ok {
		c.setMutedLocal(muted)
		e.c.log.Debug().Str("call_id", c.id).Bool("muted", muted).Msg("mute recorded locally, call not answered yet")
		return nil
	}
	if muted {
		c.setMutedLocal(true)
	}
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/src/client/voip.rs#L1746-L1770
	node := signaling.BuildMuteV2(c.id, to, creator, muteStateAttr(muted))
	node.Attrs["id"] = e.nextCallNodeID()
	if err := e.transmitCallNode(context.Background(), node); err != nil {
		e.c.log.Warn().Err(err).Str("call_id", c.id).Bool("muted", muted).Msg("send own mute_v2 failed")
		return fmt.Errorf("meowcaller: send mute_v2: %w", err)
	}
	c.setMutedLocal(muted)
	e.c.log.Info().Str("call_id", c.id).Bool("muted", muted).Msg("announced own mute state")
	return nil
}

// announceMuteOnAccept tells the device that just answered an outgoing call
// about a mute chosen while it rang.
func (e *engine) announceMuteOnAccept(c *Call) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/src/voip/facade.rs#L3870-L3875
	if c == nil || !c.IsMuted() {
		return
	}
	go func() {
		if err := e.setMuted(c, true); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", c.id).Msg("announce mute chosen while ringing failed")
		}
	}()
}
