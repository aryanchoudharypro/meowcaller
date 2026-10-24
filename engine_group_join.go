package meowcaller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/purpshell/meowcaller/signaling"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// joinWatchdog is how long a joined call may wait for the server's roster
// before the join is treated as failed.
const joinWatchdog = 20 * time.Second

// joinableMaxAge drops a joinable call nothing has reported on for this long.
// ASSUMPTION: WA clients poll the server for joinable calls (joinable client
// poll); we don't, so a call whose end we never hear about ages out instead.
const joinableMaxAge = 3 * time.Hour

// JoinableCall is a group call still going on that this account was invited to
// but isn't in: one it missed, declined or left.
type JoinableCall struct {
	CallID  string
	Creator types.JID
	// GroupJID is the group the call belongs to; empty for an ad-hoc call.
	GroupJID types.JID
	Video    bool
	// Connected is how many people were in the call when it was last seen.
	Connected int
	// Seen is when the call was last reported as going on.
	Seen time.Time
}

// JoinableCalls lists the group calls this account can join right now.
func (c *Client) JoinableCalls() []JoinableCall {
	if c == nil || c.eng == nil {
		return nil
	}
	return c.eng.joinableSnapshot(time.Now())
}

// OnJoinableCalls registers a callback fired with the full list whenever a
// group call becomes joinable or stops being joinable.
func (c *Client) OnJoinableCalls(fn func([]JoinableCall)) {
	if c == nil || c.eng == nil {
		return
	}
	c.eng.mu.Lock()
	c.eng.onJoinable = fn
	c.eng.mu.Unlock()
}

// JoinOngoingCall joins a group call from JoinableCalls. Like WA's
// joinOngoingCall it accepts the call late: the server then sends the roster
// and relay, and a participant sends the call key.
func (c *Client) JoinOngoingCall(ctx context.Context, callID string, video bool) (*Call, error) {
	if c == nil || c.eng == nil {
		return nil, errors.New("meowcaller: call signaling is unavailable")
	}
	return c.eng.joinOngoingCall(ctx, callID, video)
}

func (e *engine) joinOngoingCall(ctx context.Context, callID string, video bool) (*Call, error) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	if err := e.requireRawCallAdapter(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	joinable, ok := e.joinable[callID]
	if !ok {
		e.mu.Unlock()
		return nil, fmt.Errorf("meowcaller: no ongoing call %s to join", callID)
	}
	if e.calls[callID] != nil {
		e.mu.Unlock()
		return nil, fmt.Errorf("meowcaller: already in call %s", callID)
	}
	call := &Call{eng: e, id: callID, peer: joinable.Creator, phase: CallPhaseConnecting}
	m := e.entry(callID)
	m.call = call
	m.group = true
	m.selfLID = e.c.wa.Store.GetLID().String()
	m.peerLID = joinable.Creator.String()
	m.creator = joinable.Creator
	m.from = types.NewJID(callID, "call")
	m.direction = CallDirectionIncoming
	m.localVideo = video
	m.remoteVideo = joinable.Video
	delete(e.joinable, callID)
	delete(e.ringingOffline, callID)
	notify, list := e.onJoinable, e.joinableListLocked(time.Now())
	e.mu.Unlock()
	if notify != nil {
		notify(list)
	}

	accept, err := signaling.BuildActiveGroupAccept(callID, joinable.Creator, e.nextCallNodeID())
	if err == nil {
		err = e.transmitCallNode(ctx, accept)
	}
	if err != nil {
		e.finishCall(callID, "join_failed")
		return nil, fmt.Errorf("meowcaller: join ongoing call: %w", err)
	}
	e.c.log.Info().Str("call_id", callID).Bool("video", video).Msg("joining ongoing group call")
	go e.watchJoin(callID, call)
	return call, nil
}

// watchJoin ends a join the server never answered with a roster, so the call
// doesn't sit connecting forever.
func (e *engine) watchJoin(callID string, call *Call) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	time.Sleep(joinWatchdog)
	e.mu.Lock()
	m := e.calls[callID]
	stalled := m != nil && m.call == call && m.groupUpdate == nil
	e.mu.Unlock()
	if !stalled {
		return
	}
	e.c.log.Warn().Str("call_id", callID).Msg("ongoing group call join got no roster; giving up")
	e.finishCall(callID, CallEndJoinFailed)
}

// CallEndJoinFailed is the OnEnd reason for a JoinOngoingCall the server never
// let in, usually because the call had already ended.
const CallEndJoinFailed = "join_failed"

// noteJoinableLocked records the group call m as joinable when it ends for us
// but not for everyone: an invite we missed or declined, or a call we left
// while others stayed. Caller holds e.mu.
func (e *engine) noteJoinableLocked(callID string, m *engineCall, phase CallPhase, reason string, now time.Time) bool {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	if m == nil || !m.group || m.link || m.groupUpdate == nil || !m.groupUpdate.Joinable || m.creator.IsEmpty() {
		return false
	}
	switch reason {
	case "group_call_ended", "accepted_elsewhere", CallEndJoinFailed:
		return false
	}
	connected := connectedOthers(*m.groupUpdate, m.selfLID)
	left := phase == CallPhaseActive || phase == CallPhaseConnecting
	missed := m.direction == CallDirectionIncoming && phase == CallPhaseRinging
	if !missed && !(left && connected > 0) {
		return false
	}
	if e.joinable == nil {
		e.joinable = map[string]JoinableCall{}
	}
	e.joinable[callID] = JoinableCall{
		CallID: callID, Creator: m.creator, GroupJID: m.groupUpdate.GroupJID,
		Video: m.groupUpdate.Media == "video" || m.remoteVideo, Connected: connected, Seen: now,
	}
	return true
}

// noteOfflineJoinable records a group call we were invited to while offline
// as joinable, if its roster says it's open to latecomers and someone is in
// it. A terminate or roster further on in the offline backlog drops it again
// once the call has ended.
func (e *engine) noteOfflineJoinable(ev *events.CallOffer, update groupCallUpdate) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	creator := ev.CallCreator
	if creator.IsEmpty() {
		creator = update.CallCreator
	}
	connected := connectedOthers(update, e.c.wa.Store.GetLID().String())
	if !update.Joinable || connected == 0 || creator.IsEmpty() {
		e.c.log.Debug().Str("call_id", ev.CallID).Bool("joinable", update.Joinable).Int("connected", connected).
			Msg("offline group offer not joinable")
		return
	}
	seen := ev.Timestamp
	now := time.Now()
	if seen.IsZero() || seen.After(now) {
		seen = now
	}
	e.mu.Lock()
	if e.calls[ev.CallID] != nil {
		e.mu.Unlock()
		return
	}
	if e.joinable == nil {
		e.joinable = map[string]JoinableCall{}
	}
	groupJID := update.GroupJID
	if groupJID.IsEmpty() {
		groupJID = ev.GroupJID
	}
	if e.ringingOffline == nil {
		e.ringingOffline = map[string]bool{}
	}
	e.ringingOffline[ev.CallID] = true
	e.joinable[ev.CallID] = JoinableCall{
		CallID: ev.CallID, Creator: creator, GroupJID: groupJID,
		Video: update.Media == "video" || ev.Video, Connected: connected, Seen: seen,
	}
	notify, list := e.onJoinable, e.joinableListLocked(now)
	e.mu.Unlock()
	e.c.log.Info().Str("call_id", ev.CallID).Int("connected", connected).Msg("group call missed while offline is joinable")
	if notify != nil {
		notify(list)
	}
}

// onJoinableTerminate handles a terminate for a call we're not in: unless it
// says the call stays joinable, the call is over.
func (e *engine) onJoinableTerminate(callID, reason string, joinable bool) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	e.mu.Lock()
	ringing := e.ringingOffline[callID]
	delete(e.ringingOffline, callID)
	e.mu.Unlock()
	// The first terminate after an offline offer ends its ringing, as it
	// would for a ringing call we missed live: only these reasons end the
	// call itself.
	if ringing && reason != "group_call_ended" && reason != "accepted_elsewhere" {
		return
	}
	if joinable && reason != "group_call_ended" {
		return
	}
	e.forgetJoinable(callID, "terminate")
}

// onJoinableGroupUpdate refreshes a joinable call from a roster that arrived
// after we left it; a roster with nobody connected means the call is over.
func (e *engine) onJoinableGroupUpdate(update groupCallUpdate) bool {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	self := e.c.wa.Store.GetLID().String()
	e.mu.Lock()
	joinable, ok := e.joinable[update.CallID]
	if !ok || e.calls[update.CallID] != nil {
		e.mu.Unlock()
		return false
	}
	connected := connectedOthers(update, self)
	if connected == 0 || !update.Joinable {
		e.mu.Unlock()
		e.forgetJoinable(update.CallID, "empty_roster")
		return true
	}
	joinable.Connected = connected
	joinable.Seen = time.Now()
	if update.Media == "video" {
		joinable.Video = true
	}
	e.joinable[update.CallID] = joinable
	notify, list := e.onJoinable, e.joinableListLocked(time.Now())
	e.mu.Unlock()
	if notify != nil {
		notify(list)
	}
	return true
}

func (e *engine) forgetJoinable(callID, why string) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	e.mu.Lock()
	if _, ok := e.joinable[callID]; !ok {
		e.mu.Unlock()
		return
	}
	delete(e.joinable, callID)
	delete(e.ringingOffline, callID)
	notify, list := e.onJoinable, e.joinableListLocked(time.Now())
	e.mu.Unlock()
	e.c.log.Debug().Str("call_id", callID).Str("why", why).Msg("group call no longer joinable")
	if notify != nil {
		notify(list)
	}
}

func (e *engine) joinableSnapshot(now time.Time) []JoinableCall {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.joinableListLocked(now)
}

// joinableListLocked drops aged-out entries and lists the rest, newest first.
// Caller holds e.mu.
func (e *engine) joinableListLocked(now time.Time) []JoinableCall {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	list := make([]JoinableCall, 0, len(e.joinable))
	for id, call := range e.joinable {
		if now.Sub(call.Seen) > joinableMaxAge {
			delete(e.joinable, id)
			continue
		}
		list = append(list, call)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Seen.After(list[j].Seen) })
	return list
}

// connectedOthers counts the connected participants in update other than us.
func connectedOthers(update groupCallUpdate, selfLID string) int {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	self, _ := types.ParseJID(selfLID)
	n := 0
	for _, p := range update.Participants {
		if p.State == "connected" && p.JID.User != self.User {
			n++
		}
	}
	return n
}

// terminateIsJoinable reads a raw terminate's joinable flag, which marks a
// terminate that ends our ringing but not the call.
func terminateIsJoinable(action *waBinary.Node) bool {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	return action.AttrGetter().OptionalString("joinable") == "1"
}
