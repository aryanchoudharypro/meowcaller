package meowcaller

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/purpshell/meowcaller/signaling"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/store"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func testEngineWithIncomingCall(callID string) (*engine, *Call) {
	// sendAccept generates its stanza id via DangerousInternals().GenerateRequestID(), which
	// needs a non-nil (if disconnected) whatsmeow.Client to avoid a nil-pointer panic; actual
	// transmission still goes through the injectable eng.sendCallNode set below, same as
	// every other test in this package.
	wa := whatsmeow.NewClient(&store.Device{}, waLog.Noop)
	client := &Client{wa: wa, log: zerolog.Nop()}
	eng := &engine{c: client, calls: make(map[string]*engineCall)}
	client.eng = eng
	call := &Call{eng: eng, id: callID, peer: peerJID(), phase: CallPhaseRinging}
	eng.calls[callID] = &engineCall{
		call:      call,
		direction: CallDirectionIncoming,
		from:      peerJID(),
		creator:   creatorJID(),
	}
	return eng, call
}

// TestAnswerSendsOwnMuteV2ThenAccept covers the field bug where a call placed from
// WhatsApp Web never sent the caller's <mute_v2> that accept used to be gated on: without
// it, the callee's <accept> was stuck forever even though local media (and the app's call
// UI) already came up via maybeStartMedia in answer(), leaving the caller ringing
// unanswered. Reference capture of a genuine WhatsApp Web client (diag/captures) showed
// it sends its own <mute_v2> immediately followed by <accept> on answer, unprompted by
// anything from the peer — mute_v2 was never a signal to wait for. answer() now mirrors
// that: it sends both immediately and unconditionally.
func TestAnswerSendsOwnMuteV2ThenAccept(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")

	var mu sync.Mutex
	var sent []waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		mu.Lock()
		sent = append(sent, node)
		mu.Unlock()
		return nil
	}

	if err := call.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("sent %d call nodes, want 2 (mute_v2, accept)", len(sent))
	}

	muteChildren := sent[0].GetChildren()
	if len(muteChildren) != 1 || muteChildren[0].Tag != "mute_v2" {
		t.Fatalf("first node = %#v, want mute_v2 envelope", sent[0])
	}
	if attrs := muteChildren[0].AttrGetter(); attrs.String("call-id") != "CID" || attrs.String("mute-state") != "0" {
		t.Fatalf("mute_v2 attrs = %#v, want call-id=CID mute-state=0", muteChildren[0].Attrs)
	}

	acceptChildren := sent[1].GetChildren()
	if len(acceptChildren) != 1 || acceptChildren[0].Tag != "accept" {
		t.Fatalf("second node = %#v, want accept envelope", sent[1])
	}
	if attrs := acceptChildren[0].AttrGetter(); attrs.String("call-id") != "CID" {
		t.Fatalf("accept call-id = %q, want CID", attrs.String("call-id"))
	}

	m := eng.calls["CID"]
	if m.acceptPending {
		t.Fatal("acceptPending should be cleared once accept goes out")
	}
	if !m.acceptSent {
		t.Fatal("acceptSent should be true once accept goes out")
	}
}

// TestPeerMuteV2DoesNotDoubleSendAccept asserts that a mute_v2 arriving from the peer
// after Answer() (a normal in-call mute-state change, or a caller that also sends one)
// is purely observational and never re-triggers sendAccept — accept is already on the
// wire by the time Answer() returns.
func TestPeerMuteV2DoesNotDoubleSendAccept(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")

	var mu sync.Mutex
	var sent []waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		mu.Lock()
		sent = append(sent, node)
		mu.Unlock()
		return nil
	}

	if err := call.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	muteNode := &waBinary.Node{
		Tag: "call",
		Attrs: waBinary.Attrs{
			"from": peerJID(),
		},
		Content: []waBinary.Node{{
			Tag: "mute_v2",
			Attrs: waBinary.Attrs{
				"call-id":      "CID",
				"call-creator": creatorJID(),
				"mute-state":   "0",
			},
		}},
	}
	eng.onCallRaw(muteNode)

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("sent %d call nodes, want exactly 2 (own mute_v2 + accept from Answer, no extra accept from the peer's mute_v2)", len(sent))
	}
}

// An answer on a standard-Opus call looks like WhatsApp for Windows' does: the
// normal capability and no <voip_settings>. One that carried <voip_settings>
// was silently dropped by the server, so the caller never heard it answered.
func TestOpusCallAcceptLooksLikeAnOfficialClients(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")
	eng.calls["CID"].codec = AudioCodecOpus
	var sent []waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		sent = append(sent, node)
		return nil
	}
	if err := call.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	accept := sent[len(sent)-1].GetChildren()[0]
	if accept.Tag != "accept" {
		t.Fatalf("last node is %s, want accept", accept.Tag)
	}
	for _, child := range accept.GetChildren() {
		if child.Tag == "voip_settings" {
			t.Fatal("the accept carries <voip_settings>, which the server drops")
		}
		if child.Tag == "capability" {
			if blob, _ := child.Content.([]byte); !bytes.Equal(blob, signaling.CapabilityOffer) {
				t.Fatalf("capability %x, want the normal offer %x", blob, signaling.CapabilityOffer)
			}
		}
	}
}
