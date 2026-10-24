package meowcaller

import (
	"context"
	"testing"
	"time"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func activeGroupCall(t *testing.T) (*engine, *Call, func() []string) {
	t.Helper()
	eng, call, sent := testEngineWithGroupInvite("GCALL")
	call.setPhase(CallPhaseActive)
	return eng, call, func() []string {
		var tags []string
		for _, n := range *sent {
			for _, c := range n.GetChildren() {
				tags = append(tags, c.Tag)
			}
		}
		return tags
	}
}

func TestRequestPeerMuteSendsToTheParticipant(t *testing.T) {
	eng, call, _ := activeGroupCall(t)
	var to types.JID
	var state string
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		to, _ = node.Attrs["to"].(types.JID)
		state = node.GetChildren()[0].AttrGetter().OptionalString("request-state")
		return nil
	}
	if err := call.RequestPeerMute(context.Background(), peerJID().String()); err != nil {
		t.Fatal(err)
	}
	if to.User != peerJID().User || state != "1" {
		t.Fatalf("mute request to %v with request-state %q", to, state)
	}
	if err := call.RequestPeerMute(context.Background(), "999@lid"); err == nil {
		t.Fatal("asked someone outside the call to mute")
	}
}

func TestOnlyLinkCreatorCanRemove(t *testing.T) {
	eng, call, tags := activeGroupCall(t)
	if call.CanRemoveParticipants() {
		t.Fatal("a plain group call offered remove")
	}
	m := eng.calls["GCALL"]
	m.link = true
	m.creator = types.NewJID("111", types.HiddenUserServer)
	m.selfLID = "111@lid"
	m.groupUpdate.Participants = append(m.groupUpdate.Participants, groupCallParticipant{JID: m.creator, State: "connected"})
	if !call.CanRemoveParticipants() {
		t.Fatal("link creator can't remove")
	}
	if err := call.RemoveParticipant(context.Background(), peerJID().String()); err != nil {
		t.Fatal(err)
	}
	if got := tags(); len(got) == 0 || got[len(got)-1] != "remove_user" {
		t.Fatalf("sent %v, want remove_user", got)
	}
}

func TestMuteRequestMutesUs(t *testing.T) {
	eng, call, _ := activeGroupCall(t)
	got := make(chan types.JID, 1)
	call.OnMuteRequested(func(by types.JID) { got <- by })
	eng.onMuteRequest("GCALL", peerJID(), "1")
	select {
	case by := <-got:
		if by.User != peerJID().User {
			t.Fatalf("muted by %v", by)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mute request ignored")
	}
	if !call.IsMuted() {
		t.Fatal("still unmuted after a mute request")
	}
}

func TestWaitingRoomAdminCanRemove(t *testing.T) {
	eng, call, _ := activeGroupCall(t)
	eng.calls["GCALL"].link = true
	if call.CanRemoveParticipants() {
		t.Fatal("a link call guest who isn't an admin can remove")
	}
	call.setWaitingRoomState(WaitingRoomState{IsAdmin: true, TransactionID: 1})
	if !call.CanRemoveParticipants() {
		t.Fatal("a waiting-room admin can't remove")
	}
}
