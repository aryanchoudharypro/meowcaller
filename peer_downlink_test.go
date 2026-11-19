package meowcaller

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestPeerDownlinkBandwidthIsKeptFromRelayLatency(t *testing.T) {
	eng, call := testEngineWithOutgoingCallToDevices(nil)
	eng.calls["CID"].selfLID = "100000000000001:36@lid"
	relayLatency := func(from types.JID, dlBw string) *events.CallRelayLatency {
		te := waBinary.Node{Tag: "te", Attrs: waBinary.Attrs{"latency": "79"}, Content: []byte{1, 2, 3, 4, 5, 6}}
		if dlBw != "" {
			te.Attrs["dl_bw"] = dlBw
		}
		return &events.CallRelayLatency{
			BasicCallMeta: types.BasicCallMeta{From: from, CallCreator: creatorJID(), CallID: "CID"},
			Data:          &waBinary.Node{Tag: "relaylatency", Content: []waBinary.Node{te}},
		}
	}
	m := eng.calls["CID"]

	eng.notePeerDownlink(m, relayLatency(peerJID(), ""), relayLatency(peerJID(), "").Data)
	if got := call.MediaStats().PeerDownlinkBitrateBps; got != 0 {
		t.Fatalf("no dl_bw gave %d", got)
	}
	own := types.JID{User: "100000000000001", Device: 36, Server: types.HiddenUserServer}
	echo := relayLatency(own, "999000")
	eng.notePeerDownlink(m, echo, echo.Data)
	if got := call.MediaStats().PeerDownlinkBitrateBps; got != 0 {
		t.Fatalf("our own relaylatency set the peer's downlink to %d", got)
	}
	ev := relayLatency(peerJID(), "332670")
	eng.notePeerDownlink(m, ev, ev.Data)
	if got := call.MediaStats().PeerDownlinkBitrateBps; got != 332670 {
		t.Fatalf("peer downlink = %d, want 332670", got)
	}
}
