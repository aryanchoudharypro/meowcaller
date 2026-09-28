package meowcaller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func captureCallNodes(eng *engine) func() []waBinary.Node {
	var mu sync.Mutex
	var sent []waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		mu.Lock()
		sent = append(sent, node)
		mu.Unlock()
		return nil
	}
	return func() []waBinary.Node {
		mu.Lock()
		defer mu.Unlock()
		return append([]waBinary.Node(nil), sent...)
	}
}

func muteStates(nodes []waBinary.Node) []string {
	var states []string
	for _, node := range nodes {
		for _, child := range node.GetChildren() {
			if child.Tag == "mute_v2" {
				states = append(states, child.AttrGetter().String("mute-state"))
			}
		}
	}
	return states
}

func TestSetMutedAnnouncesOnAnsweredIncomingCall(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")
	sent := captureCallNodes(eng)
	if err := call.Answer(); err != nil {
		t.Fatal(err)
	}
	if err := call.SetMuted(true); err != nil {
		t.Fatal(err)
	}
	if !call.IsMuted() {
		t.Fatal("not muted locally")
	}
	nodes := sent()
	if got := muteStates(nodes); len(got) != 2 || got[0] != "0" || got[1] != "1" {
		t.Fatalf("mute states = %v, want [0 1] (answer, then mute)", got)
	}
	last := nodes[len(nodes)-1]
	if to, _ := last.Attrs["to"].(types.JID); to != peerJID() {
		t.Fatalf("mute addressed to %v, want the caller %v", last.Attrs["to"], peerJID())
	}
}

func TestUnmuteWaitsForTheAnnouncement(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")
	captureCallNodes(eng)
	if err := call.Answer(); err != nil {
		t.Fatal(err)
	}
	if err := call.SetMuted(true); err != nil {
		t.Fatal(err)
	}
	eng.sendCallNode = func(context.Context, waBinary.Node) error { return errors.New("offline") }
	if err := call.SetMuted(false); err == nil {
		t.Fatal("unmute reported success although the stanza failed")
	}
	if !call.IsMuted() {
		t.Fatal("microphone went live while the peer still shows it muted")
	}
}

func TestAnswerCarriesMuteChosenWhileRinging(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")
	sent := captureCallNodes(eng)
	if err := call.SetMuted(true); err != nil {
		t.Fatal(err)
	}
	if got := muteStates(sent()); len(got) != 0 {
		t.Fatalf("mute announced before answering: %v", got)
	}
	if err := call.Answer(); err != nil {
		t.Fatal(err)
	}
	if got := muteStates(sent()); len(got) != 1 || got[0] != "1" {
		t.Fatalf("answer's own mute_v2 = %v, want [1]", got)
	}
}

func TestOutgoingMuteWaitsForAccept(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")
	eng.calls["CID"].direction = CallDirectionOutgoing
	sent := captureCallNodes(eng)
	if err := call.SetMuted(true); err != nil {
		t.Fatal(err)
	}
	if got := muteStates(sent()); len(got) != 0 {
		t.Fatalf("mute announced to a call nobody answered: %v", got)
	}
	call.markPeerAccepted()
	eng.announceMuteOnAccept(call)
	deadline := time.Now().Add(2 * time.Second)
	for len(muteStates(sent())) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := muteStates(sent()); len(got) != 1 || got[0] != "1" {
		t.Fatalf("mute after accept = %v, want [1]", got)
	}
}

func TestGroupMuteAddressesTheCall(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")
	m := eng.calls["CID"]
	m.group = true
	m.from = types.NewJID("CID", "call")
	sent := captureCallNodes(eng)
	if err := call.SetMuted(true); err != nil {
		t.Fatal(err)
	}
	nodes := sent()
	if len(nodes) != 1 {
		t.Fatalf("sent %d nodes, want 1", len(nodes))
	}
	if to, _ := nodes[0].Attrs["to"].(types.JID); to != types.NewJID("CID", "call") {
		t.Fatalf("group mute addressed to %v", nodes[0].Attrs["to"])
	}
}
