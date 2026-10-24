package meowcaller

import (
	"context"
	"errors"
	"fmt"

	"github.com/purpshell/meowcaller/signaling"
	"go.mau.fi/whatsmeow/types"
)

// RequestPeerMute asks one participant of this group call to mute, like WA's
// "Mute {name}". The participant's own client mutes and announces it.
func (c *Call) RequestPeerMute(ctx context.Context, user string) error {
	return c.eng.requestPeerMute(ctx, c.id, user)
}

// RemoveParticipant removes one participant from this group call. As in WA,
// only a call-link call's creator or waiting-room admins can remove people.
func (c *Call) RemoveParticipant(ctx context.Context, user string) error {
	return c.eng.removeParticipant(ctx, c.id, user)
}

// CanRemoveParticipants reports whether RemoveParticipant is allowed here.
func (c *Call) CanRemoveParticipants() bool {
	_, err := c.eng.callAdmin(c.id)
	return err == nil
}

// OnMuteRequested registers a callback fired when another participant mutes
// this client. The call is already muted when it runs; by is empty when the
// stanza didn't say who asked.
func (c *Call) OnMuteRequested(fn func(by types.JID)) {
	c.mu.Lock()
	c.onMuteRequest = fn
	c.mu.Unlock()
}

func (e *engine) requestPeerMute(ctx context.Context, callID, user string) error {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	if err := e.requireRawCallAdapter(); err != nil {
		return err
	}
	target, err := parseCallTarget(user)
	if err != nil {
		return err
	}
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil || !m.group || m.call.State() != CallPhaseActive {
		e.mu.Unlock()
		return errors.New("meowcaller: group call is not active")
	}
	creator, update := m.creator, m.groupUpdate
	self, _ := types.ParseJID(m.selfLID)
	e.mu.Unlock()
	if target.User == self.User {
		return errors.New("meowcaller: can't ask yourself to mute")
	}
	p, ok := rosterParticipant(update, target)
	if !ok || p.State != "connected" {
		return fmt.Errorf("meowcaller: %s is not in the call", target.User)
	}
	node := signaling.BuildMuteRequest(callID, p.JID.ToNonAD(), creator, e.nextCallNodeID())
	if err := e.transmitCallNode(ctx, node); err != nil {
		e.c.log.Warn().Err(err).Str("call_id", callID).Msg("send mute request failed")
		return fmt.Errorf("meowcaller: send mute request: %w", err)
	}
	e.c.log.Info().Str("call_id", callID).Str("target", p.JID.ToNonAD().String()).Msg("asked participant to mute")
	return nil
}

func (e *engine) removeParticipant(ctx context.Context, callID, user string) error {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	if err := e.requireRawCallAdapter(); err != nil {
		return err
	}
	target, err := parseCallTarget(user)
	if err != nil {
		return err
	}
	creator, err := e.callAdmin(callID)
	if err != nil {
		return err
	}
	e.mu.Lock()
	var update *groupCallUpdate
	if m := e.calls[callID]; m != nil {
		update = m.groupUpdate
	}
	e.mu.Unlock()
	p, ok := rosterParticipant(update, target)
	if !ok || p.State != "connected" {
		return fmt.Errorf("meowcaller: %s is not in the call", target.User)
	}
	if p.JID.User == creator.User {
		return errors.New("meowcaller: can't remove yourself")
	}
	node := signaling.BuildRemoveUser(callID, creator, p.JID.ToNonAD(), e.nextCallNodeID())
	if err := e.transmitCallNode(ctx, node); err != nil {
		e.c.log.Warn().Err(err).Str("call_id", callID).Msg("send remove_user failed")
		return fmt.Errorf("meowcaller: remove participant: %w", err)
	}
	e.c.log.Info().Str("call_id", callID).Str("target", p.JID.ToNonAD().String()).Msg("asked to remove participant")
	return nil
}

// callAdmin returns the call's creator when this client may remove people:
// WA only offers it in a call-link call, to its creator or a waiting-room admin.
func (e *engine) callAdmin(callID string) (types.JID, error) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	if m == nil || m.call == nil || !m.group || m.call.State() != CallPhaseActive {
		return types.EmptyJID, errors.New("meowcaller: group call is not active")
	}
	self, _ := types.ParseJID(m.selfLID)
	room, _ := m.call.WaitingRoomState()
	creator := !m.creator.IsEmpty() && m.creator.User == self.User
	if !m.link || m.creator.IsEmpty() || (!creator && !room.IsAdmin) {
		return types.EmptyJID, errors.New("meowcaller: only the call link's creator or admins can remove people")
	}
	return m.creator, nil
}

// onMuteRequest obeys another participant's request to mute, as WA clients
// do: mute, announce it, and tell the app who asked.
func (e *engine) onMuteRequest(callID string, from types.JID, state string) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	e.mu.Lock()
	m := e.calls[callID]
	var call *Call
	group := false
	var self types.JID
	if m != nil {
		call, group = m.call, m.group
		self, _ = types.ParseJID(m.selfLID)
	}
	e.mu.Unlock()
	switch {
	case call == nil || call.State() == CallPhaseEnded:
		e.c.log.Debug().Str("call_id", callID).Msg("mute request for a call we're not in")
		return
	case !group:
		e.c.log.Debug().Str("call_id", callID).Msg("ignoring mute request in a 1:1 call")
		return
	case state != "1":
		e.c.log.Debug().Str("call_id", callID).Str("request_state", state).Msg("ignoring unknown mute request state")
		return
	case !from.IsEmpty() && from.Server != "call" && from.User == self.User:
		e.c.log.Debug().Str("call_id", callID).Msg("ignoring mute request from our own device")
		return
	}
	var by types.JID
	if from.Server != "call" {
		by = from.ToNonAD()
	}
	e.c.log.Info().Str("call_id", callID).Str("by", by.String()).Msg("muted by another participant")
	go func() {
		if err := e.setMuted(call, true); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", callID).Msg("mute on request failed")
			return
		}
		call.mu.Lock()
		fn := call.onMuteRequest
		call.mu.Unlock()
		if fn != nil {
			fn(by)
		}
	}()
}

// rosterParticipant finds target in the call's roster by LID or phone number.
func rosterParticipant(update *groupCallUpdate, target types.JID) (groupCallParticipant, bool) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	if update == nil {
		return groupCallParticipant{}, false
	}
	for _, p := range update.Participants {
		if p.JID.User == target.User || (!p.PN.IsEmpty() && p.PN.User == target.User) {
			return p, true
		}
	}
	return groupCallParticipant{}, false
}
