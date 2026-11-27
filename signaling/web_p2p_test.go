package signaling

import (
	"bytes"
	"net/netip"
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func TestWebP2PTransportRoundTrip(t *testing.T) {
	peer := types.NewJID("100000000000", types.DefaultUserServer)
	in := &WebP2PTransport{
		CallID: "CALLID", Round: "1", ICEUfrag: "ufrag", ICEPwd: "pwd",
		CertAlgorithm: "sha-256", CertFingerprint: "AA:BB",
		Candidates: []WebP2PCandidate{
			{IP: netip.MustParseAddr("192.0.2.7"), Port: 50000, Priority: 126},
			{IP: netip.MustParseAddr("2001:db8::1"), Port: 3478, Priority: 100},
		},
	}
	node := BuildWebP2PTransport(peer, peer, in, "2")
	tr := node.GetChildren()[0]
	kids := tr.GetChildren()
	if len(kids) != 4 || kids[0].Tag != "te" || kids[1].Tag != "te" || kids[2].Tag != "net" || kids[3].Tag != "certificate" {
		t.Fatalf("child order: %v", kids)
	}
	if got := kids[0].Content.([]byte); !bytes.Equal(got, []byte{192, 0, 2, 7, 0xc3, 0x50}) {
		t.Fatalf("ipv4 te = %x", got)
	}
	if len(kids[1].Content.([]byte)) != 18 {
		t.Fatalf("ipv6 te len %d", len(kids[1].Content.([]byte)))
	}
	out, err := ParseWebP2PTransport(node)
	if err != nil {
		t.Fatal(err)
	}
	if out.ICEUfrag != in.ICEUfrag || out.ICEPwd != in.ICEPwd || out.CertFingerprint != in.CertFingerprint || out.Round != "1" || len(out.Candidates) != 2 || out.Candidates[1] != in.Candidates[1] {
		t.Fatalf("round trip: %+v", out)
	}
}

func TestWebP2PTransportCandidateOnly(t *testing.T) {
	node := waBinary.Node{Tag: "transport", Attrs: waBinary.Attrs{"transport-message-type": "13"}, Content: []waBinary.Node{
		{Tag: "te", Attrs: waBinary.Attrs{"priority": "90"}, Content: []byte{198, 51, 100, 1, 0x0d, 0x96}},
		{Tag: "te", Content: []byte{1, 2, 3}},
	}}
	out, err := ParseWebP2PTransport(node)
	if err != nil {
		t.Fatal(err)
	}
	if out.ICEUfrag != "" || len(out.Candidates) != 1 || out.Candidates[0].Port != 3478 || out.Candidates[0].Priority != 90 {
		t.Fatalf("got %+v", out)
	}
	if _, err := ParseWebP2PTransport(waBinary.Node{Tag: "transport", Attrs: waBinary.Attrs{"transport-message-type": "4"}}); err == nil {
		t.Fatal("type 4 accepted")
	}
}
