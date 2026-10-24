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
		if !isCallAction(node, "transport") {
			sent = append(sent, node)
		}
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
		if !isCallAction(node, "transport") {
			sent = append(sent, node)
		}
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
		if !isCallAction(node, "transport") {
			sent = append(sent, node)
		}
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

func isCallAction(node waBinary.Node, tag string) bool {
	children := node.GetChildren()
	return len(children) == 1 && children[0].Tag == tag
}

// TestOutgoingAcceptSendsCallerTransportOnce: as the caller we answer the callee's
// <accept> with our <transport>, addressed to the answering device and carrying an id
// on the <call> wrapper, once per call.
func TestOutgoingAcceptSendsCallerTransportOnce(t *testing.T) {
	answering := peerJID()
	answering.Device = 14
	eng, _ := testEngineWithOutgoingCallToDevices([]types.JID{answering})

	var sent []waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		if isCallAction(node, "transport") {
			sent = append(sent, node)
		}
		return nil
	}
	for i := 0; i < 2; i++ {
		eng.onAccept(&events.CallAccept{
			BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: answering},
			Data:          &waBinary.Node{Tag: "accept"},
		})
	}
	if len(sent) != 1 {
		t.Fatalf("sent %d transports across two accepts, want 1", len(sent))
	}
	wrapper := sent[0].AttrGetter()
	if wrapper.JID("to") != answering || wrapper.OptionalString("id") == "" {
		t.Fatalf("transport wrapper = %#v, want to=answering device with an id", sent[0].Attrs)
	}
	action := sent[0].GetChildren()[0].AttrGetter()
	if action.String("call-id") != "CID" || action.JID("call-creator") != creatorJID() ||
		action.String("p2p-cand-round") != "1" || action.String("transport-message-type") != "1" {
		t.Fatalf("transport attrs = %#v", sent[0].GetChildren()[0].Attrs)
	}
}

// TestOfferAckSendsCallerRelayLatency: the offer ack's relay is announced to the
// callee devices the ack lists, with the server's c2r_rtt as the latency.
func TestOfferAckSendsCallerRelayLatency(t *testing.T) {
	device := peerJID()
	device.Device = 14
	eng, _ := testEngineWithOutgoingCallToDevices([]types.JID{device})
	eng.calls["CID"].relay = &relayData{
		relayTokens: [][]byte{[]byte("tok")},
		endpoints: []relayEndpoint{{
			relayName: "gru1c02", authTokenID: 1, c2rRTTMs: 33,
			te2: []byte{157, 240, 226, 133, 0x0d, 0x96},
		}},
	}
	var sent []waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		sent = append(sent, node)
		return nil
	}
	ack := &waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"class": "call", "type": "offer"}, Content: []waBinary.Node{
		{Tag: "relay", Content: []waBinary.Node{{Tag: "device", Attrs: waBinary.Attrs{"jid": types.NewJID("999", types.HiddenUserServer)}}}},
		{Tag: "user", Content: []waBinary.Node{{Tag: "device", Attrs: waBinary.Attrs{"jid": device}}}},
	}}
	if got := offerAckDevices(ack); len(got) != 1 || got[0] != device {
		t.Fatalf("offerAckDevices = %v, want [%s] (relay block skipped)", got, device)
	}
	eng.sendCallerRelayLatency("CID", ack)
	if len(sent) != 1 || !isCallAction(sent[0], "relaylatency") {
		t.Fatalf("sent %#v, want one relaylatency", sent)
	}
	if sent[0].AttrGetter().JID("to") != peerJID() || sent[0].AttrGetter().OptionalString("id") == "" {
		t.Fatalf("relaylatency wrapper = %#v", sent[0].Attrs)
	}
	rl := sent[0].GetChildren()[0]
	te := rl.GetChildren()[0].AttrGetter()
	if te.String("relay_name") != "gru1c02" || te.String("latency") != "33554465" {
		t.Fatalf("te attrs = %#v", rl.GetChildren()[0].Attrs)
	}
	if dest := rl.GetChildren(); len(dest) != 2 || dest[1].Tag != "destination" {
		t.Fatalf("relaylatency children = %#v, want te + destination", dest)
	}
}
