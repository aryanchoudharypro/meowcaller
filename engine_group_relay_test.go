package meowcaller

import (
	"bytes"
	"errors"
	"testing"

	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/stun"
	"go.mau.fi/whatsmeow/types"
)

func TestGroupRelayDataSelectsCaptureAddressAndCredentials(t *testing.T) {
	update := groupCallUpdate{Relay: &groupCallRelay{
		Key:    bytes.Repeat([]byte{0x21}, 16),
		Tokens: [][]byte{bytes.Repeat([]byte{0x42}, 174)},
		Endpoints: []groupCallRelayEndpoint{{
			RelayID: 7, RelayName: "zrh1c01", TokenID: 0,
			IPv4: "157.240.17.62", Port: 3478,
		}},
	}}
	rd, err := groupRelayData(update, false)
	if err != nil {
		t.Fatalf("groupRelayData: %v", err)
	}
	endpoint := getMediaRelayEndpoint(rd, false)
	if endpoint == nil || endpoint.relayName != "zrh1c01" ||
		endpoint.addresses[0].ipv4 != "157.240.17.62" ||
		endpoint.addresses[0].port != 3478 {
		t.Fatalf("selected endpoint = %+v", endpoint)
	}
	if !bytes.Equal(rd.relayKeyASCII, update.Relay.Key) ||
		!bytes.Equal(rd.relayTokens[0], update.Relay.Tokens[0]) {
		t.Fatal("group relay credentials were not copied")
	}
	rd.relayKeyASCII[0] ^= 0xff
	if bytes.Equal(rd.relayKeyASCII, update.Relay.Key) {
		t.Fatal("relay key aliases the signaling snapshot")
	}
}

func TestGroupRelayAllocateStateCarriesParticipantSubscriptions(t *testing.T) {
	initial := []byte{0x00, 0x01, 0x02}
	key := bytes.Repeat([]byte{0x24}, 16)
	token := bytes.Repeat([]byte{0x42}, 174)
	hbhFEC := [2]uint32{0x11223344, 0x55667788}
	state := newGroupRelayAllocateStateWithHBHFEC(initial, key, hbhFEC)
	endpoint := &relayEndpoint{
		relayName: "zrh1c01",
		addresses: []relayAddress{{ipv4: "157.240.17.62", port: 3478}},
	}
	relayUpdate := &groupCallRelay{
		TransactionID: 4, Key: key, Tokens: [][]byte{token},
		Endpoints: []groupCallRelayEndpoint{{RelayName: "zrh1c01", TokenID: 0}},
	}
	streamSSRCs := [9]uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}
	appDataSSRC := uint32(10)
	pids := []uint32{31, 47}
	transactionID := [12]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	var sent []byte
	changed, err := state.ApplyWithSubscriptions(
		endpoint,
		relayUpdate,
		streamSSRCs,
		appDataSSRC,
		pids,
		transactionID,
		func(packet []byte) error {
			sent = bytes.Clone(packet)
			return nil
		},
	)
	if err != nil || !changed {
		t.Fatalf("ApplyWithSubscriptions = (%v, %v), want (true, nil)", changed, err)
	}
	endpointXOR, _ := stun.EncodeXorRelayEndpoint("157.240.17.62", 3478)
	want := stun.BuildWasmStunAllocateRequestWithGroupSubscriptionsAndHBHFEC(
		transactionID, token, endpointXOR, streamSSRCs, appDataSSRC, hbhFEC, pids, key,
	)
	if !bytes.Equal(sent, want) {
		t.Fatalf("group allocate = %x, want %x", sent, want)
	}
}

func TestGroupRelayAllocateSendFailureDoesNotCommitRotation(t *testing.T) {
	initial := []byte{0x00, 0x01, 0x02}
	initialKey := bytes.Repeat([]byte{0x12}, 16)
	state := newGroupRelayAllocateStateWithHBHFEC(initial, initialKey, [2]uint32{})
	endpoint := &relayEndpoint{
		relayName: "zrh1c01",
		addresses: []relayAddress{{ipv4: "157.240.17.62", port: 3478}},
	}
	relayUpdate := &groupCallRelay{
		TransactionID: 1,
		Key:           bytes.Repeat([]byte{0x24}, 16),
		Tokens:        [][]byte{bytes.Repeat([]byte{0x42}, 174)},
		Endpoints:     []groupCallRelayEndpoint{{RelayName: "zrh1c01", TokenID: 0}},
	}
	sendErr := errors.New("relay unavailable")
	changed, err := state.ApplyWithSubscriptions(
		endpoint, relayUpdate, [9]uint32{}, 0, nil, [12]byte{},
		func([]byte) error { return sendErr },
	)
	if !errors.Is(err, sendErr) || changed {
		t.Fatalf("failed ApplyWithSubscriptions = (%v, %v)", changed, err)
	}
	if got := state.Current(); !bytes.Equal(got, initial) {
		t.Fatalf("current allocate after failure = %x, want %x", got, initial)
	}
}

func TestConnectedRemoteParticipantPIDsExcludesSelfAndInactive(t *testing.T) {
	self := types.JID{User: "100", Server: types.HiddenUserServer, Device: 1}
	remote := types.JID{User: "200", Server: types.HiddenUserServer, Device: 2}
	inactive := types.JID{User: "300", Server: types.HiddenUserServer, Device: 3}
	update := groupCallUpdate{Participants: []groupCallParticipant{
		{State: "connected", Devices: []groupCallDevice{{JID: self, PID: 11, HasPID: true}}},
		{State: "connected", Devices: []groupCallDevice{{JID: remote, PID: 22, HasPID: true}}},
		{State: "ringing", Devices: []groupCallDevice{{JID: inactive, PID: 33, HasPID: true}}},
	}}
	got := connectedRemoteParticipantPIDs(update, rtp.FormatE2ESrtpParticipantID(self.String()))
	if len(got) != 1 || got[0] != 22 {
		t.Fatalf("connected remote PIDs = %v, want [22]", got)
	}
}

func TestSelectGroupRelayEndpointFollowsTheAllocationsFirstRelay(t *testing.T) {
	// A 1:1 call bound to del3c02/bom5c02/maa5c02 that became a group call whose
	// allocation lists del2c03 first and still carries bom5c02. Both phones
	// moved to del2c03; staying on bom5c02 left the call silent (live capture,
	// 2026-10-03).
	bound := []relayEndpoint{
		{relayName: "del3c02", addresses: []relayAddress{{ipv4: "57.144.49.57", port: 3478}}},
		{relayName: "bom5c02", addresses: []relayAddress{{ipv4: "57.144.43.57", port: 3478}}},
		{relayName: "maa5c02", addresses: []relayAddress{{ipv4: "57.144.57.57", port: 3478}}},
	}
	token := []byte{0x42}
	group := &relayData{
		relayTokens: [][]byte{token, token, token},
		endpoints: []relayEndpoint{
			{relayName: "del2c03", tokenID: 1, addresses: []relayAddress{{ipv4: "57.144.147.57", port: 3478}}},
			{relayName: "bom5c02", tokenID: 2, addresses: []relayAddress{{ipv4: "57.144.43.57", port: 3478}}},
		},
	}
	target, alreadyBound, ok := selectGroupRelayEndpoint(bound, group)
	if !ok || alreadyBound || target.relayName != "del2c03" || target.tokenID != 1 {
		t.Fatalf("target = %+v alreadyBound=%v ok=%v, want a redial to del2c03", target, alreadyBound, ok)
	}

	// The allocation's first relay is one the call is already on: no redial,
	// but the endpoint carries the group token.
	group.endpoints[0], group.endpoints[1] = group.endpoints[1], group.endpoints[0]
	target, alreadyBound, ok = selectGroupRelayEndpoint(bound, group)
	if !ok || !alreadyBound || target.relayName != "bom5c02" || target.tokenID != 2 {
		t.Fatalf("target = %+v alreadyBound=%v ok=%v, want the bound bom5c02 with the group token", target, alreadyBound, ok)
	}

	// Same relay name on a different address is a different socket.
	group.endpoints[0].addresses = []relayAddress{{ipv4: "57.144.43.58", port: 3478}}
	if target, alreadyBound, ok = selectGroupRelayEndpoint(bound, group); !ok || alreadyBound {
		t.Fatalf("target = %+v alreadyBound=%v ok=%v, want a redial for the moved address", target, alreadyBound, ok)
	}

	// Endpoints without a token, and FNA ones, are skipped.
	group.endpoints = []relayEndpoint{
		{relayName: "fna1c01", isFNA: true, addresses: []relayAddress{{ipv4: "10.0.0.1", port: 3478}}},
		{relayName: "ccu2c02", tokenID: 9, addresses: []relayAddress{{ipv4: "10.0.0.2", port: 3478}}},
		{relayName: "maa5c01", tokenID: 0, addresses: []relayAddress{{ipv4: "10.0.0.3", port: 3478}}},
	}
	if target, _, ok = selectGroupRelayEndpoint(bound, group); !ok || target.relayName != "maa5c01" {
		t.Fatalf("target = %+v ok=%v, want maa5c01", target, ok)
	}
}

func TestGroupRelayAllocateStatePendingTracksTheRelayTransaction(t *testing.T) {
	key := bytes.Repeat([]byte{0x24}, 16)
	state := newGroupRelayAllocateStateWithHBHFEC([]byte{0x00}, key, [2]uint32{1, 2})
	if state.HasGroup() {
		t.Fatal("a fresh state already claims a group allocation")
	}
	relayUpdate := &groupCallRelay{
		TransactionID: 1, Key: key, Tokens: [][]byte{bytes.Repeat([]byte{0x42}, 174)},
		Endpoints: []groupCallRelayEndpoint{{RelayName: "del2c02", TokenID: 0}},
	}
	if state.Pending(nil) || !state.Pending(relayUpdate) {
		t.Fatal("the first group relay must be pending, and no relay must not be")
	}
	endpoint := &relayEndpoint{
		relayName: "del2c02",
		addresses: []relayAddress{{ipv4: "163.70.145.133", port: 3478}},
	}
	if _, err := state.ApplyWithSubscriptions(
		endpoint, relayUpdate, [9]uint32{}, 10, nil, [12]byte{}, func([]byte) error { return nil },
	); err != nil {
		t.Fatalf("ApplyWithSubscriptions: %v", err)
	}
	if !state.HasGroup() || state.Pending(relayUpdate) {
		t.Fatal("an applied relay transaction is still pending")
	}
	relayUpdate.TransactionID = 2
	if !state.Pending(relayUpdate) {
		t.Fatal("a newer relay transaction must be pending")
	}
}
