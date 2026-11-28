package signaling

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func TestParseGroupCallReminderType(t *testing.T) {
	creator := types.NewJID("1", types.HiddenUserServer)
	node := waBinary.Node{Tag: "group_call_reminder", Attrs: waBinary.Attrs{
		"call-id": "C1", "call-creator": creator, "ts": "1700000000", "type": "link_creator_waiting_room_joined",
	}}
	r, err := ParseGroupCallReminder(&node, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Type != ReminderWaitingRoomJoined || r.Timestamp != 1700000000 || r.CallID != "C1" {
		t.Fatalf("unexpected %+v", r)
	}
}

func TestBuildDuration(t *testing.T) {
	creator := types.NewJID("1", types.HiddenUserServer)
	n := BuildDuration(&DurationParams{CallID: "C1", CallCreator: creator, Peer: creator, Type: "1x1", AudioSeconds: 65, VideoSeconds: 0}, "r1")
	d := n.GetChildren()[0]
	if n.Attrs["to"] != types.NewJID("", "call") || d.Tag != "duration" || d.Attrs["audio_duration"] != "65" || d.Attrs["type"] != "1x1" {
		t.Fatalf("unexpected %v", n)
	}
}

func TestBuildCancelOffer(t *testing.T) {
	creator := types.NewJID("1", types.HiddenUserServer)
	user := types.NewJID("2", types.HiddenUserServer)
	n := BuildCancelOffer("C1", creator, []types.JID{user}, "r1")
	c := n.GetChildren()[0]
	if c.Tag != "cancel_offer" || len(c.GetChildren()) != 1 || c.GetChildren()[0].Attrs["jid"] != user {
		t.Fatalf("unexpected %v", n)
	}
}
