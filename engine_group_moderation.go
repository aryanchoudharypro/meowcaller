package meowcaller

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// ErrPeerAlreadyMuted is returned by RequestPeerMute when the participant's
// microphone is already off (WA: "{name} is already muted.").
var ErrPeerAlreadyMuted = errors.New("meowcaller: participant is already muted")

// ErrMuteRequestPending is returned by RequestPeerMute while an earlier request
// to the same participant has not been answered yet.
var ErrMuteRequestPending = errors.New("meowcaller: already asked this participant to mute")

// muteRequestTimeout is how long an unanswered mute request blocks the next one.
// ASSUMPTION: WA clears mute_requested only on the peer's mute_v2; 10 s keeps a
// lost answer from blocking the button for the rest of the call.
const muteRequestTimeout = 10 * time.Second

// ParticipantMuted reports whether a group participant's last mute_v2 said
// their microphone is off. user is a LID or phone-number JID string.
func (c *Call) ParticipantMuted(user string) bool {
	target, err := parseCallTarget(user)
	if err != nil {
		return false
	}
	c.eng.mu.Lock()
	defer c.eng.mu.Unlock()
	m := c.eng.calls[c.id]
	if m == nil {
		return false
	}
	if p, ok := rosterParticipant(m.groupUpdate, target); ok {
		return m.participantMuted[p.JID.User] || (!p.PN.IsEmpty() && m.participantMuted[p.PN.User])
	}
	return m.participantMuted[target.User]
}

// OnParticipantMuteState registers a callback fired when a group participant
// turns their microphone on or off. user is the JID the mute_v2 came from.
func (c *Call) OnParticipantMuteState(fn func(user types.JID, muted bool)) {
	c.mu.Lock()
	c.onParticipantMute = fn
	c.mu.Unlock()
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
	// Source of truth: WA VoIP wasm wa_call_request_peer_mute_in_group_call returns 70020 ("already muted" / "mute request pending").
	if err := e.claimMuteRequest(callID, p); err != nil {
		e.c.log.Debug().Err(err).Str("call_id", callID).Str("target", p.JID.ToNonAD().String()).Msg("not asking participant to mute")
		return err
	}
	node := signaling.BuildMuteRequest(callID, p.JID.ToNonAD(), creator, e.nextCallNodeID())
	if err := e.transmitCallNode(ctx, node); err != nil {
		e.dropMuteRequest(callID, p.JID.User)
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

// claimMuteRequest refuses a mute request WA's engine would refuse (70020):
// the participant is already muted, or was asked a moment ago and hasn't
// answered. Otherwise it records the request.
func (e *engine) claimMuteRequest(callID string, p groupCallParticipant) error {
	// Source of truth: WA VoIP wasm wa_call_request_peer_mute_in_group_call (participant is_muted, mute_requested).
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	if m == nil {
		return errors.New("meowcaller: group call is not active")
	}
	if m.participantMuted[p.JID.User] || (!p.PN.IsEmpty() && m.participantMuted[p.PN.User]) {
		return ErrPeerAlreadyMuted
	}
	if at, ok := m.muteAskedAt[p.JID.User]; ok && time.Since(at) < muteRequestTimeout {
		return ErrMuteRequestPending
	}
	if m.muteAskedAt == nil {
		m.muteAskedAt = map[string]time.Time{}
	}
	m.muteAskedAt[p.JID.User] = time.Now()
	return nil
}

func (e *engine) dropMuteRequest(callID, user string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if m := e.calls[callID]; m != nil {
		delete(m.muteAskedAt, user)
	}
}

// onParticipantMuteV2 records a group participant's mute_v2 and reports
// whether the stanza was a group participant's (true) rather than a 1:1 peer's.
// WA's handle_mute_v2 ignores one from our own other device.
func (e *engine) onParticipantMuteV2(callID string, from types.JID, muted bool) bool {
	// Source of truth: WA VoIP wasm handle_mute_v2 ("ignoring MuteV2 from self-other-device").
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || !m.group || m.call == nil {
		e.mu.Unlock()
		return false
	}
	self, _ := types.ParseJID(m.selfLID)
	if from.IsEmpty() || from.Server == "call" || from.User == self.User {
		e.mu.Unlock()
		e.c.log.Debug().Str("call_id", callID).Msg("ignoring group mute_v2 without a participant sender")
		return true
	}
	user := from.ToNonAD()
	if p, ok := rosterParticipant(m.groupUpdate, user); ok {
		user = p.JID.ToNonAD()
	}
	if m.participantMuted == nil {
		m.participantMuted = map[string]bool{}
	}
	changed := m.participantMuted[user.User] != muted
	m.participantMuted[user.User] = muted
	delete(m.muteAskedAt, user.User)
	call := m.call
	e.mu.Unlock()
	if !changed {
		return true
	}
	call.mu.Lock()
	fn := call.onParticipantMute
	call.mu.Unlock()
	if fn != nil {
		fn(user, muted)
	}
	return true
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
