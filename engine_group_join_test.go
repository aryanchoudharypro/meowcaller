package meowcaller

import (
	"context"
	"sync"
	"testing"
	"time"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func testEngineWithGroupInvite(callID string) (*engine, *Call, *[]waBinary.Node) {
	eng, call := testEngineWithIncomingCall(callID)
	m := eng.calls[callID]
	m.group = true
	m.from = types.NewJID(callID, "call")
	m.groupUpdate = &groupCallUpdate{
		CallID: callID, CallCreator: creatorJID(), Joinable: true, Media: "audio",
		GroupJID:      types.NewJID("120363000000000001", types.GroupServer),
		TransactionID: 3,
		Participants: []groupCallParticipant{
			{JID: creatorJID(), State: "connected"},
			{JID: peerJID(), State: "connected"},
		},
	}
	var mu sync.Mutex
	var sent []waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		mu.Lock()
		sent = append(sent, node)
		mu.Unlock()
		return nil
	}
	return eng, call, &sent
}

func TestDecliningGroupInviteMakesItJoinable(t *testing.T) {
	eng, call, _ := testEngineWithGroupInvite("GCALL")
	var got []JoinableCall
	eng.c.OnJoinableCalls(func(list []JoinableCall) { got = list })
	if err := call.Reject(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].CallID != "GCALL" || got[0].GroupJID.User != "120363000000000001" || got[0].Connected != 2 {
		t.Fatalf("joinable after decline = %+v", got)
	}

	// A terminate that keeps the call joinable changes nothing; one that doesn't ends it.
	eng.onJoinableTerminate("GCALL", "timeout", true)
	if len(eng.c.JoinableCalls()) != 1 {
		t.Fatal("joinable terminate dropped the call")
	}
	eng.onJoinableTerminate("GCALL", "", false)
	if len(got) != 0 || len(eng.c.JoinableCalls()) != 0 {
		t.Fatalf("call still joinable after it ended: %+v", got)
	}
}

func TestAcceptedElsewhereIsNotJoinable(t *testing.T) {
	eng, _, _ := testEngineWithGroupInvite("GCALL")
	eng.finishCall("GCALL", "accepted_elsewhere")
	if len(eng.c.JoinableCalls()) != 0 {
		t.Fatal("a call answered on another device became joinable")
	}
}

func TestEmptyRosterEndsJoinableCall(t *testing.T) {
	eng, call, _ := testEngineWithGroupInvite("GCALL")
	_ = call.Reject()
	if !eng.onJoinableGroupUpdate(groupCallUpdate{CallID: "GCALL", Joinable: true, TransactionID: 9}) {
		t.Fatal("roster for a joinable call wasn't consumed")
	}
	if len(eng.c.JoinableCalls()) != 0 {
		t.Fatal("call with nobody connected is still joinable")
	}
}

func TestJoinOngoingCallAcceptsLate(t *testing.T) {
	eng, call, sent := testEngineWithGroupInvite("GCALL")
	_ = call.Reject()
	*sent = nil
	joined, err := eng.c.JoinOngoingCall(context.Background(), "GCALL", false)
	if err != nil {
		t.Fatal(err)
	}
	if joined.State() != CallPhaseConnecting {
		t.Fatalf("joined call phase %v", joined.State())
	}
	if len(*sent) != 1 {
		t.Fatalf("sent %d nodes, want the accept", len(*sent))
	}
	node := (*sent)[0]
	if node.Attrs["to"] != types.NewJID("GCALL", "call") || node.GetChildren()[0].Tag != "accept" {
		t.Fatalf("join sent %v", node)
	}
	if len(eng.c.JoinableCalls()) != 0 {
		t.Fatal("a joined call is still listed as joinable")
	}
	if _, err = eng.c.JoinOngoingCall(context.Background(), "GCALL", false); err == nil {
		t.Fatal("joined the same call twice")
	}
}

func TestJoinableCallsAgeOut(t *testing.T) {
	eng, call, _ := testEngineWithGroupInvite("GCALL")
	_ = call.Reject()
	if got := eng.joinableSnapshot(time.Now().Add(joinableMaxAge + time.Minute)); len(got) != 0 {
		t.Fatalf("stale joinable call kept: %+v", got)
	}
}
