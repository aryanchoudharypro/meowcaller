package meowcaller

import (
	"context"
	"sync"
	"testing"
	"time"

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

// TestDeferredAcceptFallbackFiresWhenMuteV2NeverArrives covers the field bug where a call
// placed from WhatsApp Web never sends the <mute_v2> that sendAccept is normally gated on
// (Android callers do): without a fallback, the callee's <accept> is stuck forever even
// though local media (and the app's call UI) already came up via maybeStartMedia in
// answer(), leaving the caller ringing unanswered.
func TestDeferredAcceptFallbackFiresWhenMuteV2NeverArrives(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")
	eng.acceptFallbackDelay = time.Millisecond

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
	if !eng.calls["CID"].acceptPending {
		t.Fatal("acceptPending should be set immediately by Answer")
	}

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("fallback never sent the deferred accept")
		case <-time.After(time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("sent %d call nodes, want 1", len(sent))
	}
	children := sent[0].GetChildren()
	if len(children) != 1 || children[0].Tag != "accept" {
		t.Fatalf("accept envelope = %#v", sent[0])
	}
	if attrs := children[0].AttrGetter(); attrs.String("call-id") != "CID" {
		t.Fatalf("accept call-id = %q, want CID", attrs.String("call-id"))
	}

	m := eng.calls["CID"]
	if m.acceptPending {
		t.Fatal("acceptPending should be cleared once the fallback accept goes out")
	}
	if !m.acceptSent {
		t.Fatal("acceptSent should be true once the fallback accept goes out")
	}
}

// TestDeferredAcceptFallbackSkippedWhenMuteV2ArrivesFirst asserts the fallback is a no-op
// once the real <mute_v2> has already sent the accept — the common case (Android callers)
// must not end up sending <accept> twice.
func TestDeferredAcceptFallbackSkippedWhenMuteV2ArrivesFirst(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")
	eng.acceptFallbackDelay = 50 * time.Millisecond

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

	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("sent %d call nodes, want exactly 1 (no duplicate accept from the fallback)", len(sent))
	}
}
