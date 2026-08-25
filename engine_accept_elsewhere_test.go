package meowcaller

import (
	"context"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func testEngineWithOutgoingCallToDevices(devices []types.JID) (*engine, *Call) {
	// See testEngineWithIncomingCall: GenerateRequestID needs a non-nil (if disconnected)
	// whatsmeow.Client, actual transmission goes through the injectable eng.sendCallNode.
	wa := whatsmeow.NewClient(&store.Device{}, waLog.Noop)
	client := &Client{wa: wa, log: zerolog.Nop()}
	eng := &engine{c: client, calls: make(map[string]*engineCall)}
	client.eng = eng
	call := &Call{eng: eng, id: "CID", peer: peerJID(), phase: CallPhaseCalling}
	eng.calls["CID"] = &engineCall{
		call:           call,
		direction:      CallDirectionOutgoing,
		from:           peerJID(),
		creator:        creatorJID(),
		peerLID:        peerJID().String(),
		peerBareJID:    peerJID(),
		offeredDevices: devices,
	}
	return eng, call
}

// TestOutgoingAcceptNotifiesOtherOfferedDevices covers the other half of the WhatsApp Web
// call-answer bug: reference capture of a genuine WhatsApp Web client placing a call
// (diag/captures) shows that once one of the callee's devices accepts, the caller sends
// <terminate reason="accepted_elsewhere"> addressed to the callee's other devices — that
// is what silences their local ring UI (their phone, most visibly). Without it, calls
// placed from meowcaller left every other device the callee owned ringing until it
// separately timed out or was dismissed by hand, even though the call itself connected
// fine on the device that answered.
func TestOutgoingAcceptNotifiesOtherOfferedDevices(t *testing.T) {
	answering := peerJID()
	answering.Device = 14
	sibling := peerJID()
	sibling.Device = 15
	bare := peerJID()

	eng, _ := testEngineWithOutgoingCallToDevices([]types.JID{bare, sibling, answering})

	var mu sync.Mutex
	var sent []waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		mu.Lock()
		sent = append(sent, node)
		mu.Unlock()
		return nil
	}

	eng.onAccept(&events.CallAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: answering},
		Data:          &waBinary.Node{Tag: "accept"},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("sent %d call nodes, want 1 (terminate to the other devices)", len(sent))
	}
	children := sent[0].GetChildren()
	if len(children) != 1 || children[0].Tag != "terminate" {
		t.Fatalf("node = %#v, want terminate envelope", sent[0])
	}
	term := children[0]
	if attrs := term.AttrGetter(); attrs.String("call-id") != "CID" || attrs.String("reason") != "accepted_elsewhere" {
		t.Fatalf("terminate attrs = %#v, want call-id=CID reason=accepted_elsewhere", term.Attrs)
	}
	dest := term.GetChildren()
	if len(dest) != 1 || dest[0].Tag != "destination" {
		t.Fatalf("terminate content = %#v, want a single destination child", term.Content)
	}
	tos := dest[0].GetChildren()
	if len(tos) != 2 {
		t.Fatalf("destination has %d <to>, want 2 (bare + sibling, excluding the answering device)", len(tos))
	}
	got := map[string]bool{}
	for _, to := range tos {
		got[to.AttrGetter().JID("jid").String()] = true
	}
	if !got[bare.String()] || !got[sibling.String()] {
		t.Fatalf("destination jids = %v, want %s and %s", got, bare, sibling)
	}
	if got[answering.String()] {
		t.Fatalf("destination included the answering device %s, should have been excluded", answering)
	}

	eng.mu.Lock()
	notified := eng.calls["CID"].notifiedOtherDevices
	eng.mu.Unlock()
	if !notified {
		t.Fatal("notifiedOtherDevices should be set after sending the terminate")
	}
}

// TestOutgoingAcceptNotifiesOtherDevicesOnlyOnce guards against a second accept (e.g. a
// duplicate delivery, or the answering device re-confirming) re-sending the terminate.
func TestOutgoingAcceptNotifiesOtherDevicesOnlyOnce(t *testing.T) {
	answering := peerJID()
	answering.Device = 14
	sibling := peerJID()
	sibling.Device = 15

	eng, _ := testEngineWithOutgoingCallToDevices([]types.JID{sibling, answering})

	var mu sync.Mutex
	var sent []waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		mu.Lock()
		sent = append(sent, node)
		mu.Unlock()
		return nil
	}

	for i := 0; i < 2; i++ {
		eng.onAccept(&events.CallAccept{
			BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: answering},
			Data:          &waBinary.Node{Tag: "accept"},
		})
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("sent %d call nodes across two accepts, want exactly 1 terminate", len(sent))
	}
}

// TestOutgoingAcceptSkipsNotifyWhenOnlyOneDeviceWasOffered asserts a single-device callee
// (the common case) never gets a spurious empty-destination terminate.
func TestOutgoingAcceptSkipsNotifyWhenOnlyOneDeviceWasOffered(t *testing.T) {
	answering := peerJID()
	answering.Device = 14

	eng, _ := testEngineWithOutgoingCallToDevices([]types.JID{answering})

	var mu sync.Mutex
	var sent []waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		mu.Lock()
		sent = append(sent, node)
		mu.Unlock()
		return nil
	}

	eng.onAccept(&events.CallAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: answering},
		Data:          &waBinary.Node{Tag: "accept"},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 0 {
		t.Fatalf("sent %d call nodes, want 0 (no other devices to notify)", len(sent))
	}
}
