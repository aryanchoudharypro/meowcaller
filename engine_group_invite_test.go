package meowcaller

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func inviteSnapshot() groupCallUpdate {
	device := func(user string) []groupCallDevice {
		return []groupCallDevice{{JID: types.NewJID(user, types.HiddenUserServer)}}
	}
	return groupCallUpdate{
		Media: "audio", ConnectedLimit: 32,
		Participants: []groupCallParticipant{
			{JID: types.NewJID("100", types.HiddenUserServer), State: "connected", Devices: device("100")},
			{JID: types.NewJID("200", types.HiddenUserServer), State: "connected", Devices: device("200")},
			{
				JID: types.NewJID("300", types.HiddenUserServer), State: "invited", Devices: device("300"),
				PN: types.NewJID("919999", types.DefaultUserServer),
			},
		},
	}
}

func TestGroupInviteOfferRosterCarriesOnlyConnectedParticipants(t *testing.T) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d9f78b806f1f4ca80c8008caa5846e5d542c2c55/src/voip/facade.rs#L3518-L3563
	snapshot := inviteSnapshot()
	target := types.NewJID("400", types.HiddenUserServer)
	roster, err := groupInviteOfferRoster(snapshot, target, target, false)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if len(roster) != 2 || roster[0].JID.User != "100" || roster[1].JID.User != "200" {
		t.Fatalf("invite roster = %+v, want the two connected participants", roster)
	}

	// Someone already on the roster is rung, not invited, also when named by phone number.
	member := types.NewJID("300", types.HiddenUserServer)
	byPhone := types.NewJID("919999", types.DefaultUserServer)
	if _, err = groupInviteOfferRoster(snapshot, member, byPhone, false); err == nil {
		t.Fatal("inviting a roster member must fail")
	}
	unresolved := types.NewJID("555", types.HiddenUserServer)
	if _, err = groupInviteOfferRoster(snapshot, unresolved, byPhone, false); err == nil {
		t.Fatal("a roster member named by phone number must be recognized")
	}

	roster, err = groupInviteOfferRoster(snapshot, member, member, true)
	if err != nil || len(roster) != 2 {
		t.Fatalf("ring roster = %+v, err = %v", roster, err)
	}
	if _, err = groupInviteOfferRoster(snapshot, target, target, true); err == nil {
		t.Fatal("ringing someone outside the roster must fail")
	}
	connected := types.NewJID("200", types.HiddenUserServer)
	if _, err = groupInviteOfferRoster(snapshot, connected, connected, true); err == nil {
		t.Fatal("ringing a connected participant must fail")
	}
}

func TestGroupInviteOfferRosterEnforcesLimits(t *testing.T) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d9f78b806f1f4ca80c8008caa5846e5d542c2c55/src/voip/facade.rs#L3480-L3498
	snapshot := inviteSnapshot()
	snapshot.ConnectedLimit = 2
	target := types.NewJID("400", types.HiddenUserServer)
	if _, err := groupInviteOfferRoster(snapshot, target, target, false); err == nil {
		t.Fatal("a call at its connected limit must refuse an invite")
	}
	snapshot.ConnectedLimit = 0
	for len(snapshot.Participants) < groupCallMaxParticipants {
		snapshot.Participants = append(snapshot.Participants, groupCallParticipant{
			JID: types.NewJID("9", types.HiddenUserServer), State: "invited",
		})
	}
	if _, err := groupInviteOfferRoster(snapshot, target, target, false); err == nil {
		t.Fatal("a full roster must refuse an invite")
	}
}

func TestDropHostedDevicesRemovesCloudAPICompanions(t *testing.T) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d9f78b806f1f4ca80c8008caa5846e5d542c2c55/src/voip/facade.rs#L1227-L1230
	regular := types.NewJID("333", types.HiddenUserServer)
	companion := types.NewADJID("333", 0, 7)
	companion.Server = types.HiddenUserServer
	hostedDevice := types.NewADJID("333", 0, 99)
	hostedDevice.Server = types.HiddenUserServer
	hostedServer := types.NewJID("444", types.HostedServer)
	hostedLID := types.NewJID("444", types.HostedLIDServer)

	kept := dropHostedDevices([]types.JID{regular, hostedDevice, companion, hostedServer, hostedLID})
	if len(kept) != 2 || kept[0] != regular || kept[1] != companion {
		t.Fatalf("kept = %v, want only the regular devices", kept)
	}
}
