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

func TestMuteRequestRefusedWhenMutedOrPending(t *testing.T) {
	eng, call, _ := activeGroupCall(t)
	sends := 0
	eng.sendCallNode = func(context.Context, waBinary.Node) error { sends++; return nil }
	if err := call.RequestPeerMute(context.Background(), peerJID().String()); err != nil {
		t.Fatal(err)
	}
	if err := call.RequestPeerMute(context.Background(), peerJID().String()); err != ErrMuteRequestPending {
		t.Fatalf("second request: %v, want pending", err)
	}
	changes := make(chan bool, 2)
	call.OnParticipantMuteState(func(user types.JID, muted bool) {
		if user.User == peerJID().User {
			changes <- muted
		}
	})
	if !eng.onParticipantMuteV2("GCALL", peerJID(), true) {
		t.Fatal("group mute_v2 not taken as a participant's")
	}
	if !call.ParticipantMuted(peerJID().String()) || !<-changes {
		t.Fatal("participant not marked muted")
	}
	if err := call.RequestPeerMute(context.Background(), peerJID().String()); err != ErrPeerAlreadyMuted {
		t.Fatalf("request to a muted participant: %v", err)
	}
	eng.onParticipantMuteV2("GCALL", peerJID(), false)
	if call.ParticipantMuted(peerJID().String()) || <-changes {
		t.Fatal("participant still muted")
	}
	if err := call.RequestPeerMute(context.Background(), peerJID().String()); err != nil {
		t.Fatal(err)
	}
	if sends != 2 {
		t.Fatalf("sent %d mute requests, want 2", sends)
	}
}

func TestGroupAckVoipSettingsPickCodec(t *testing.T) {
	eng, call, _ := activeGroupCall(t)
	m := eng.calls["GCALL"]
	m.localCodec, m.codec = AudioCodecMlow, AudioCodecMlow
	call.negotiatedOpus.Store(false)
	ack := &waBinary.Node{Tag: "ack", Content: []waBinary.Node{
		{Tag: "group_info"},
		{Tag: "voip_settings", Attrs: waBinary.Attrs{"uncompressed": "1"}, Content: []byte(`{"options":{"use_mlow_codec_v1":"false"}}`)},
	}}
	eng.applyGroupAckVoipSettings("GCALL", ack)
	if m.codec != AudioCodecOpus || !call.sendsStandardOpus() {
		t.Fatalf("codec %v after the group ack, want opus", m.codec)
	}
}

func TestNewParticipantIsToldOurMuteState(t *testing.T) {
	eng, _, _ := activeGroupCall(t)
	var sent []string
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		to, _ := node.Attrs["to"].(types.JID)
		kid := node.GetChildren()[0]
		sent = append(sent, kid.Tag+">"+to.User+":"+kid.AttrGetter().OptionalString("mute-state"))
		return nil
	}
	m := eng.calls["GCALL"]
	m.selfLID = "111@lid"
	m.groupUpdate.Participants = []groupCallParticipant{
		{JID: types.NewJID("111", types.HiddenUserServer), State: "connected"},
		{JID: peerJID(), State: "connected"},
		{JID: creatorJID(), State: "invited"},
	}
	eng.announceMuteToNewPeers("GCALL")
	if len(sent) != 1 || sent[0] != "mute_v2>"+peerJID().User+":0" {
		t.Fatalf("sent %v", sent)
	}
	eng.announceMuteToNewPeers("GCALL")
	if len(sent) != 1 {
		t.Fatalf("told the same participant twice: %v", sent)
	}
}

func TestOnlySetupAckErrorsEndTheCall(t *testing.T) {
	for ackType, want := range map[string]bool{
		"": true, "offer": true, "accept": true, "preaccept": true,
		"video": false, "mute_v2": false, "relaylatency": false, "terminate": false,
	} {
		if got := callAckErrorEndsCall(ackType); got != want {
			t.Errorf("callAckErrorEndsCall(%q) = %v, want %v", ackType, got, want)
		}
	}
}

func TestGroupCallStaysJoinableAcrossRosters(t *testing.T) {
	first := groupCallUpdate{CallID: "GROUP", TransactionID: 9, Joinable: true}
	later := groupCallUpdate{CallID: "GROUP", TransactionID: 32}
	merged, outcome := mergeGroupUpdate(&first, later)
	if outcome != groupUpdateApplied || !merged.Joinable {
		t.Fatalf("merged = %+v outcome = %v, want a joinable applied roster", merged, outcome)
	}
}

func TestGroupCallVideoUsesTheGroupSignaling(t *testing.T) {
	eng, call, _ := activeGroupCall(t)
	var sent []string
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		kid := node.GetChildren()[0]
		sent = append(sent, kid.Tag+":"+kid.AttrGetter().OptionalString("state")+":"+kid.AttrGetter().OptionalString("voip_settings"))
		return nil
	}
	if err := call.StartVideo(); err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	if m := eng.calls["GCALL"]; !m.localVideo || m.videoGate {
		t.Fatalf("localVideo=%v videoGate=%v, want our video on with no gate", m.localVideo, m.videoGate)
	}
	if err := call.SetVideoEnabled(false); err != nil {
		t.Fatalf("SetVideoEnabled(false): %v", err)
	}
	if err := call.StopVideo(); err != nil {
		t.Fatalf("StopVideo: %v", err)
	}
	if err := call.SetVideoEnabled(true); err != nil {
		t.Fatalf("SetVideoEnabled(true): %v", err)
	}
	if len(sent) != 2 || sent[0] != "video:3:" || sent[1] != "video:3:" {
		t.Fatalf("sent %v, want two plain upgrade requests and nothing for video off", sent)
	}
}
