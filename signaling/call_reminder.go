package signaling

import (
	"fmt"
	"strconv"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// ReminderType is the server's reason for a <group_call_reminder>.
type ReminderType int

// Reminder types, in WA's order (WAWebVoipWaCallEnums.ServerReminderType).
const (
	ReminderLegacy ReminderType = iota
	ReminderLinkJoinedCreator
	ReminderLinkJoinedJoiner
	ReminderLinkCreatorCallConnected
	ReminderLinkCreatorCallMissed
	ReminderWaitingRoomJoined
	ReminderWaitingRoomMultipleJoined
)

var reminderTypeNames = map[string]ReminderType{
	"link_creator_call_started":                 ReminderLinkJoinedCreator,
	"link_joiner_call_started":                  ReminderLinkJoinedJoiner,
	"link_creator_call_connected":               ReminderLinkCreatorCallConnected,
	"link_creator_call_missed":                  ReminderLinkCreatorCallMissed,
	"link_creator_waiting_room_joined":          ReminderWaitingRoomJoined,
	"link_creator_waiting_room_multiple_joined": ReminderWaitingRoomMultipleJoined,
}

// GroupCallReminder is a parsed <group_call_reminder>.
type GroupCallReminder struct {
	CallID      string
	CallCreator types.JID
	Type        ReminderType
	TypeName    string
	Timestamp   int64
	LinkToken   string
	Group       *GroupCallUpdate // nil when absent
	WaitingRoom *WaitingRoom     // nil when absent
}

// ParseGroupCallReminder parses a <group_call_reminder>; parent is the
// enclosing <call>, which may carry the type instead.
func ParseGroupCallReminder(node, parent *waBinary.Node) (*GroupCallReminder, error) {
	// Source of truth: wasm deserialize_group_call_reminder (func 14097, group_call.cc): call-id, call-creator, ts, waiting_room, group_info, type mapped through a 7-name table (func 13864).
	if node == nil || node.Tag != "group_call_reminder" {
		return nil, fmt.Errorf("signaling: parse group call reminder: unexpected node")
	}
	attrs := node.AttrGetter()
	r := &GroupCallReminder{CallID: attrs.String("call-id"), CallCreator: attrs.JID("call-creator")}
	if err := attrs.Error(); err != nil {
		return nil, fmt.Errorf("signaling: parse group call reminder identity: %w", err)
	}
	if ts, ok := node.Attrs["ts"].(string); ok && ts != "" {
		v, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("signaling: parse group call reminder ts: %w", err)
		}
		r.Timestamp = v
	}
	r.TypeName, _ = node.Attrs["type"].(string)
	if r.TypeName == "" && parent != nil {
		r.TypeName, _ = parent.Attrs["type"].(string)
	}
	r.Type = reminderTypeNames[r.TypeName]
	if child, ok := node.GetOptionalChildByTag("waiting_room"); ok {
		room, err := parseWaitingRoomNode(&child)
		if err != nil {
			return nil, fmt.Errorf("signaling: parse group call reminder waiting room: %w", err)
		}
		r.WaitingRoom = room
		r.LinkToken = room.LinkToken
	}
	if info, ok := node.GetOptionalChildByTag("group_info"); ok {
		update := &GroupCallUpdate{CallID: r.CallID, CallCreator: r.CallCreator}
		if err := parseGroupInfo(&info, update); err != nil {
			return nil, fmt.Errorf("signaling: parse group call reminder group info: %w", err)
		}
		r.Group = update
		if r.LinkToken == "" {
			r.LinkToken, _ = info.Attrs["link-token"].(string)
		}
	}
	return r, nil
}
