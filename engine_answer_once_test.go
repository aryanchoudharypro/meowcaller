package meowcaller

import (
	"context"
	"sync"
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
)

func recordCallNodes(eng *engine) func() []string {
	var mu sync.Mutex
	var tags []string
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		mu.Lock()
		defer mu.Unlock()
		for _, child := range node.GetChildren() {
			tags = append(tags, child.Tag)
		}
		return nil
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), tags...)
	}
}

// A user pressing Answer again while the call connects sent a fresh <accept>
// each time, and the server's 500 error ack to one of them ended the call.
func TestAnswerTwiceSendsOneAccept(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")
	sent := recordCallNodes(eng)
	for range 3 {
		if err := call.Answer(); err != nil {
			t.Fatalf("Answer: %v", err)
		}
	}
	got := sent()
	if len(got) != 2 || got[0] != "mute_v2" || got[1] != "accept" {
		t.Fatalf("sent %v, want [mute_v2 accept]", got)
	}
}

func TestRejectBeforeAnswerSendsReject(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")
	sent := recordCallNodes(eng)
	if err := call.Reject(); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if got := sent(); len(got) != 1 || got[0] != "reject" {
		t.Fatalf("sent %v, want [reject]", got)
	}
}

func TestRejectAfterAnswerTerminates(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")
	sent := recordCallNodes(eng)
	if err := call.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if err := call.Reject(); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	got := sent()
	if len(got) != 3 || got[2] != "terminate" {
		t.Fatalf("sent %v, want [mute_v2 accept terminate]", got)
	}
	if call.State() != CallPhaseEnded {
		t.Fatalf("call state %v, want ended", call.State())
	}
}
