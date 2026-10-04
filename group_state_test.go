package meowcaller

import (
	"bytes"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func groupStateTestRelay(transactionID uint32, name string) *groupCallRelay {
	return &groupCallRelay{
		TransactionID: transactionID,
		Key:           bytes.Repeat([]byte{byte(transactionID)}, 16),
		Tokens:        [][]byte{{0x42}},
		Endpoints:     []groupCallRelayEndpoint{{RelayName: name, IPv4: "10.0.0.1", Port: 3478}},
	}
}

func groupStateTestDevice(user string, device uint16, pid uint32, hasPID bool) groupCallDevice {
	return groupCallDevice{
		JID: types.JID{User: user, Server: types.HiddenUserServer, Device: device},
		PID: pid, HasPID: hasPID,
	}
}

func groupStateTestParticipant(user, state string, devices ...groupCallDevice) groupCallParticipant {
	return groupCallParticipant{
		JID:   types.JID{User: user, Server: types.HiddenUserServer},
		State: state, Devices: devices,
	}
}

func TestMergeGroupUpdateInheritsOmittedPIDsAndTheHeldRelay(t *testing.T) {
	current := groupCallUpdate{
		CallID: "CALL", TransactionID: 13,
		GroupJID: types.JID{User: "120363", Server: types.GroupServer},
		Participants: []groupCallParticipant{
			groupStateTestParticipant("100", "connected", groupStateTestDevice("100", 0, 0, true)),
			groupStateTestParticipant("200", "connected", groupStateTestDevice("200", 3, 1, true)),
		},
		Relay: groupStateTestRelay(1, "del2c03"),
	}
	// A third person joins. The update names only the new pid and no relay.
	update := groupCallUpdate{
		CallID: "CALL", TransactionID: 15,
		Participants: []groupCallParticipant{
			groupStateTestParticipant("100", "connected", groupStateTestDevice("100", 0, 0, false)),
			groupStateTestParticipant("200", "connected", groupStateTestDevice("200", 3, 0, false)),
			groupStateTestParticipant("300", "connected", groupStateTestDevice("300", 0, 2, true)),
			groupStateTestParticipant("400", "ringing", groupStateTestDevice("400", 0, 0, false)),
		},
	}
	merged, outcome := mergeGroupUpdate(&current, update)
	if outcome != groupUpdateApplied || merged.TransactionID != 15 {
		t.Fatalf("outcome = %v at transaction %d, want applied at 15", outcome, merged.TransactionID)
	}
	want := []struct {
		pid    uint32
		hasPID bool
	}{{0, true}, {1, true}, {2, true}, {0, false}}
	for i, participant := range merged.Participants {
		device := participant.Devices[0]
		if device.PID != want[i].pid || device.HasPID != want[i].hasPID {
			t.Fatalf("participant %d pid = (%d, %v), want (%d, %v)", i, device.PID, device.HasPID, want[i].pid, want[i].hasPID)
		}
	}
	if merged.Relay == nil || merged.Relay.TransactionID != 1 || merged.Relay.Endpoints[0].RelayName != "del2c03" {
		t.Fatalf("held relay was not carried across a roster-only update: %+v", merged.Relay)
	}
	if merged.GroupJID != current.GroupJID {
		t.Fatalf("group JID = %s, want the held %s", merged.GroupJID, current.GroupJID)
	}
	merged.Relay.Key[0] ^= 0xff
	if bytes.Equal(merged.Relay.Key, current.Relay.Key) {
		t.Fatal("the merged relay aliases the previous snapshot")
	}
	if update.Participants[1].Devices[0].HasPID {
		t.Fatal("merge mutated the incoming update")
	}
	if pids := connectedRemoteParticipantPIDs(merged, "none"); len(pids) != 3 {
		t.Fatalf("subscription pids = %v, want every connected device", pids)
	}
}

func TestMergeGroupUpdateAdoptsARelayFromAnOlderRoster(t *testing.T) {
	// Live: the snapshot carrying the relay (tx=13) arrives after a newer
	// roster-only one (tx=15).
	current := groupCallUpdate{
		CallID: "CALL", TransactionID: 15, Media: "video",
		Participants: []groupCallParticipant{
			groupStateTestParticipant("100", "connected", groupStateTestDevice("100", 0, 0, true)),
			groupStateTestParticipant("300", "connected", groupStateTestDevice("300", 0, 3, true)),
		},
	}
	older := groupCallUpdate{
		CallID: "CALL", TransactionID: 13, Media: "audio", RekeyRequested: true,
		Participants: []groupCallParticipant{
			groupStateTestParticipant("100", "connected", groupStateTestDevice("100", 0, 0, true)),
		},
		Relay: groupStateTestRelay(1, "del2c03"),
	}
	merged, outcome := mergeGroupUpdate(&current, older)
	if outcome != groupUpdateRelayOnly {
		t.Fatalf("outcome = %v, want relay-only", outcome)
	}
	if merged.TransactionID != 15 || merged.Media != "video" || merged.RekeyRequested ||
		len(merged.Participants) != 2 {
		t.Fatalf("an older roster replaced the committed one: %+v", merged)
	}
	if merged.Relay == nil || merged.Relay.Endpoints[0].RelayName != "del2c03" {
		t.Fatalf("the first relay allocation was not adopted: %+v", merged.Relay)
	}

	// The same allocation again changes nothing.
	if _, outcome = mergeGroupUpdate(&merged, older); outcome != groupUpdateStale {
		t.Fatalf("a repeated relay allocation = %v, want stale", outcome)
	}
	// A newer allocation on that same old roster moves the relay only.
	older.Relay = groupStateTestRelay(2, "bom5c02")
	moved, outcome := mergeGroupUpdate(&merged, older)
	if outcome != groupUpdateRelayOnly || moved.Relay.Endpoints[0].RelayName != "bom5c02" ||
		moved.TransactionID != 15 {
		t.Fatalf("a newer relay on an old roster = %v %+v", outcome, moved.Relay)
	}

	// A fresh roster cannot roll the allocation back.
	next := current
	next.TransactionID = 16
	next.Relay = groupStateTestRelay(1, "del2c03")
	rolled, outcome := mergeGroupUpdate(&moved, next)
	if outcome != groupUpdateApplied || rolled.Relay.TransactionID != 2 ||
		rolled.Relay.Endpoints[0].RelayName != "bom5c02" {
		t.Fatalf("a fresh roster rolled the relay back: %v %+v", outcome, rolled.Relay)
	}
}

func TestMergeGroupUpdateInheritsAPIDAcrossThePNAlias(t *testing.T) {
	lid := types.JID{User: "200", Server: types.HiddenUserServer}
	pn := types.JID{User: "15550002222", Server: types.DefaultUserServer}
	current := groupCallUpdate{
		CallID: "CALL", TransactionID: 4,
		Participants: []groupCallParticipant{{
			JID: lid, PN: pn, State: "connected",
			Devices: []groupCallDevice{{JID: types.JID{User: lid.User, Server: lid.Server, Device: 7}, PID: 5, HasPID: true}},
		}},
	}
	update := groupCallUpdate{
		CallID: "CALL", TransactionID: 5,
		Participants: []groupCallParticipant{{
			JID: lid, PN: pn, State: "connected",
			Devices: []groupCallDevice{{JID: types.JID{User: pn.User, Server: pn.Server, Device: 7}}},
		}},
	}
	merged, outcome := mergeGroupUpdate(&current, update)
	device := merged.Participants[0].Devices[0]
	if outcome != groupUpdateApplied || !device.HasPID || device.PID != 5 {
		t.Fatalf("pid across the PN alias = (%d, %v), outcome %v", device.PID, device.HasPID, outcome)
	}
	if device.JID.Server != types.DefaultUserServer {
		t.Fatalf("the stored device JID was rewritten to %s", device.JID)
	}
	// Another device of the same account does not inherit it.
	update.Participants[0].Devices[0].JID.Device = 8
	merged, _ = mergeGroupUpdate(&current, update)
	if merged.Participants[0].Devices[0].HasPID {
		t.Fatal("a different device inherited a pid")
	}
}

func TestApplyGroupUpdateKeepsRelayOnlyAdoptionOffTheRoster(t *testing.T) {
	eng, _, creator := testGroupEngine("GROUP")
	roster := func(transactionID uint32, relay *groupCallRelay) groupCallUpdate {
		return groupCallUpdate{
			CallID: "GROUP", CallCreator: creator, TransactionID: transactionID,
			Media: "audio", ConnectedLimit: 32, Relay: relay,
			Participants: []groupCallParticipant{
				groupStateTestParticipant("900", "connected", groupStateTestDevice("900", 0, 0, true)),
			},
		}
	}
	if _, outcome := eng.applyGroupUpdate(roster(15, nil)); outcome != groupUpdateApplied {
		t.Fatalf("first roster = %v, want applied", outcome)
	}
	committed, outcome := eng.applyGroupUpdate(roster(13, groupStateTestRelay(1, "del2c03")))
	if outcome != groupUpdateRelayOnly || committed.TransactionID != 15 || committed.Relay == nil {
		t.Fatalf("older roster with the relay = %v %+v", outcome, committed)
	}
	eng.mu.Lock()
	stored := eng.calls["GROUP"].groupUpdate
	eng.mu.Unlock()
	if stored == nil || stored.TransactionID != 15 || stored.Relay == nil {
		t.Fatalf("stored snapshot = %+v, want roster 15 with the relay", stored)
	}
	if _, outcome = eng.applyGroupUpdate(roster(14, nil)); outcome != groupUpdateStale {
		t.Fatalf("an older roster with no relay = %v, want stale", outcome)
	}
}
