package meowcaller

import (
	"time"

	"github.com/purpshell/meowcaller/signaling"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// CallReminderType is why the server sent a call reminder.
type CallReminderType = signaling.ReminderType

// Call reminder types (WA's ServerReminderType).
const (
	CallReminderLegacy                    = signaling.ReminderLegacy
	CallReminderLinkJoinedCreator         = signaling.ReminderLinkJoinedCreator
	CallReminderLinkJoinedJoiner          = signaling.ReminderLinkJoinedJoiner
	CallReminderLinkCreatorCallConnected  = signaling.ReminderLinkCreatorCallConnected
	CallReminderLinkCreatorCallMissed     = signaling.ReminderLinkCreatorCallMissed
	CallReminderWaitingRoomJoined         = signaling.ReminderWaitingRoomJoined
	CallReminderWaitingRoomMultipleJoined = signaling.ReminderWaitingRoomMultipleJoined
)

// CallReminder is a server push about a call link: someone joined it, or is
// waiting to be let in.
type CallReminder struct {
	CallID       string
	CallCreator  types.JID
	Type         CallReminderType
	TypeName     string
	Time         time.Time
	LinkToken    string
	Video        bool
	Participants []types.JID // members from group_info
	WaitingUsers []WaitingRoomUser
}

// OnCallReminder registers a callback for call reminders. WA Web shows them
// as notifications: "joined your call link" (Join call) or "is waiting"
// (Open waiting room); it ignores ones without a link token.
func (c *Client) OnCallReminder(fn func(CallReminder)) {
	if c == nil || c.eng == nil {
		return
	}
	c.eng.mu.Lock()
	c.eng.onReminder = fn
	c.eng.mu.Unlock()
}

func (e *engine) onGroupCallReminder(callNode, node *waBinary.Node) {
	// Source of truth: WA Web WAWebVoipHandleNativeCallEventCallLinkHandlers.handleGroupCallReminder (2026-11-27 bundle) over wasm deserialize_group_call_reminder (func 14097).
	r, err := signaling.ParseGroupCallReminder(node, callNode)
	if err != nil {
		e.c.log.Warn().Err(err).Msg("parse group call reminder failed")
		return
	}
	out := CallReminder{CallID: r.CallID, CallCreator: r.CallCreator, Type: r.Type, TypeName: r.TypeName, LinkToken: r.LinkToken}
	if r.Timestamp > 0 {
		out.Time = time.Unix(r.Timestamp, 0)
	}
	if r.WaitingRoom != nil {
		out.Video = r.WaitingRoom.Media == signaling.CallLinkMediaVideo
		for _, u := range r.WaitingRoom.Users {
			out.WaitingUsers = append(out.WaitingUsers, WaitingRoomUser{JID: u.JID, PN: u.PN, State: u.State, PushName: u.PushName, Guest: u.Guest})
		}
	}
	if r.Group != nil {
		if r.Group.Media == "video" {
			out.Video = true
		}
		for _, p := range r.Group.Participants {
			out.Participants = append(out.Participants, p.JID)
		}
	}
	e.c.log.Info().Str("call_id", out.CallID).Str("reminder_type", out.TypeName).
		Bool("has_link_token", out.LinkToken != "").Int("participants", len(out.Participants)).
		Int("waiting_users", len(out.WaitingUsers)).Msg("group call reminder")
	e.mu.Lock()
	fn := e.onReminder
	e.mu.Unlock()
	if fn != nil {
		fn(out)
	}
}
