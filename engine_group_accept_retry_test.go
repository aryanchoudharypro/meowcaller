package meowcaller

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func rosterWithSelf(callID string, txn uint32, state string) groupCallUpdate {
	return groupCallUpdate{
		CallID: callID, CallCreator: creatorJID(), TransactionID: txn, Media: "audio",
		Participants: []groupCallParticipant{
			{JID: creatorJID(), State: "connected"},
			{JID: peerJID(), State: state},
		},
	}
}

// A ringing 1:1 call that the caller turns into a group call by adding someone
// drops the <accept> we sent to the caller alone. WA Web sends the accept again
// when the roster that follows still lists us as not connected.
func TestUpgradedRingingCallResendsAcceptToTheCall(t *testing.T) {
	eng, call := testEngineWithIncomingCall("CID")
	eng.calls["CID"].selfLID = peerJID().String()
	sent := recordCallNodes(eng)
	if err := call.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	eng.applyGroupUpdate(rosterWithSelf("CID", 2, "invited"))
	got := sent()
	if len(got) != 3 || got[0] != "mute_v2" || got[1] != "accept" || got[2] != "accept" {
		t.Fatalf("sent %v, want [mute_v2 accept accept]", got)
	}
	if to := eng.calls["CID"].from; to != types.NewJID("CID", "call") {
		t.Fatalf("call signaling target = %s, want CID@call", to)
	}
}

func TestRosterListingUsConnectedDoesNotResendAccept(t *testing.T) {
	eng, call, _ := testEngineWithGroupInvite("CID")
	eng.calls["CID"].selfLID = peerJID().String()
	sent := recordCallNodes(eng)
	if err := call.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	eng.applyGroupUpdate(rosterWithSelf("CID", 4, "connected"))
	if got := sent(); len(got) != 1 || got[0] != "accept" {
		t.Fatalf("sent %v, want [accept]", got)
	}
}

func TestGroupInviteIsNotAnsweredUntilAnswer(t *testing.T) {
	eng, _, _ := testEngineWithGroupInvite("CID")
	eng.calls["CID"].selfLID = peerJID().String()
	sent := recordCallNodes(eng)
	eng.applyGroupUpdate(rosterWithSelf("CID", 4, "invited"))
	if got := sent(); len(got) != 0 {
		t.Fatalf("sent %v before the user answered", got)
	}
}

func TestResendingAcceptIsBounded(t *testing.T) {
	eng, call, _ := testEngineWithGroupInvite("CID")
	eng.calls["CID"].selfLID = peerJID().String()
	sent := recordCallNodes(eng)
	if err := call.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	for txn := uint32(4); txn < 14; txn++ {
		eng.applyGroupUpdate(rosterWithSelf("CID", txn, "invited"))
	}
	if got := len(sent()); got != 1+maxGroupAcceptRetries {
		t.Fatalf("sent %d accepts, want %d", got, 1+maxGroupAcceptRetries)
	}
}
