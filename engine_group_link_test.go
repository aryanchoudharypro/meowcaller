package meowcaller

import (
	"testing"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func testLinkEngine(t *testing.T, creator types.JID, withRemote bool) *engine {
	t.Helper()
	self := types.NewADJID("100", 0, 48)
	self.Server = types.HiddenUserServer
	wa := whatsmeow.NewClient(&store.Device{LID: self}, waLog.Noop)
	eng := newEngine(&Client{wa: wa, log: zerolog.Nop()})
	update := groupCallUpdate{
		CallID: "LINK", CallCreator: creator, TransactionID: 6,
		Participants: []groupCallParticipant{{
			JID: self.ToNonAD(), State: "connected",
			Devices: []groupCallDevice{{JID: self, PID: 0, HasPID: true}},
		}},
	}
	if withRemote {
		remote := types.NewADJID("200", 0, 20)
		remote.Server = types.HiddenUserServer
		update.Participants = append(update.Participants, groupCallParticipant{
			JID: remote.ToNonAD(), State: "connected",
			Devices: []groupCallDevice{{JID: remote, PID: 1, HasPID: true}},
		})
	}
	eng.calls["LINK"] = &engineCall{group: true, link: true, creator: creator, groupUpdate: &update}
	return eng
}

func TestLinkCallMediaIsGatedUntilItsFirstEpoch(t *testing.T) {
	eng := testLinkEngine(t, types.NewJID("100", types.HiddenUserServer), true)
	if !eng.groupKeyPending("LINK") {
		t.Fatal("a link call without an epoch should gate outbound audio")
	}
	eng.calls["LINK"].hasGroupEpoch = true
	if eng.groupKeyPending("LINK") {
		t.Fatal("a keyed link call should send audio")
	}
	eng.calls["LINK"].hasGroupEpoch = false
	eng.calls["LINK"].link = false
	if eng.groupKeyPending("LINK") {
		t.Fatal("only link calls start media ahead of their epoch")
	}
}

func TestLinkEpochFallbackArmsOnlyForTheCreatorWithSomeoneToKey(t *testing.T) {
	creator := types.NewADJID("100", 0, 48)
	creator.Server = types.HiddenUserServer
	cases := []struct {
		name       string
		creator    types.JID
		withRemote bool
		hasEpoch   bool
		want       bool
	}{
		{"creator with a joiner", creator, true, false, true},
		{"creator alone", creator, false, false, false},
		{"already keyed", creator, true, true, false},
		{"someone else's link", types.NewJID("300", types.HiddenUserServer), true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := testLinkEngine(t, tc.creator, tc.withRemote)
			eng.calls["LINK"].hasGroupEpoch = tc.hasEpoch
			eng.maybeScheduleLinkEpochFallback("LINK")
			got := eng.calls["LINK"].linkEpochFallbackSet
			// The armed timer finds the call gone and does nothing.
			delete(eng.calls, "LINK")
			if got != tc.want {
				t.Fatalf("armed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLinkRelayLatencyRepliesEchoOfferedRelays(t *testing.T) {
	guest := types.NewJID("300", types.HiddenUserServer)
	creator := types.NewADJID("100", 0, 48)
	creator.Server = types.HiddenUserServer
	probe := waBinary.Node{
		Tag: "relaylatency",
		Attrs: waBinary.Attrs{
			"call-id": "LINK", "call-creator": creator, "transaction-id": "3",
		},
		Content: []waBinary.Node{
			{Tag: "te", Attrs: waBinary.Attrs{"latency": "33555290", "relay_name": "del3c01"}, Content: []byte{1, 2, 3, 4, 5, 6}},
			{Tag: "te", Attrs: waBinary.Attrs{"latency": "33555696", "relay_name": "elsewhere"}, Content: []byte{6, 5, 4, 3, 2, 1}},
		},
	}
	ev := &events.CallRelayLatency{
		BasicCallMeta: types.BasicCallMeta{From: guest, CallCreator: creator, CallID: "LINK"},
	}
	replies := linkRelayLatencyReplies(ev, &probe, map[string]bool{"del3c01": true})
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1 (only the offered relay)", len(replies))
	}
	reply := replies[0]
	if reply.Attrs["to"] != guest {
		t.Fatalf("reply to = %v, want the guest", reply.Attrs["to"])
	}
	action := reply.GetChildren()[0]
	if action.Tag != "relaylatency" || action.Attrs["transaction-id"] != "3" {
		t.Fatalf("action = %+v", action)
	}
	te := action.GetChildren()[0]
	if te.Attrs["relay_name"] != "del3c01" || te.Attrs["latency"] != "33555290" {
		t.Fatalf("te = %+v", te)
	}
}
