package signaling

import (
	"bytes"
	"testing"

	"github.com/rs/zerolog"
	waBinary "go.mau.fi/whatsmeow/binary"
)

func TestCapabilityBitAtReadsIndex31(t *testing.T) {
	cases := []struct {
		name    string
		version uint32
		hasVer  bool
		blob    []byte
		want    CapabilityBit
	}{
		{"our own offer blob announces mlow", 1, true, CapabilityOffer, CapabilitySet},
		{"video offer blob announces mlow", 1, true, CapabilityVideoOffer, CapabilitySet},
		{"preaccept blob announces mlow", 1, true, CapabilityPreaccept, CapabilitySet},
		{"standard-opus blob clears it", 1, true, CapabilityStandardOpusOffer, CapabilityClear},
		// The #1105 shape: a blob whose own embedded version byte is not 1. Reading
		// the mask anyway would answer Set and keep MLow against an Opus peer.
		{"embedded version byte wrong", 1, true, []byte{0x00, 0x05, 0xf7, 0x09, 0xe0, 0xbb, 0x13}, CapabilityClear},
		{"ver attribute missing", 0, false, CapabilityOffer, CapabilityClear},
		{"ver attribute below the index version", 0, true, CapabilityOffer, CapabilityClear},
		{"declared length past the content", 1, true, []byte{0x01, 0x09, 0xf7, 0x09, 0xe0, 0xbb, 0x13}, CapabilityClear},
		{"empty blob is clear, not unknown", 1, true, []byte{}, CapabilityClear},
		{"header only", 1, true, []byte{0x01, 0x00}, CapabilityClear},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CapabilityBitAt(tc.version, tc.hasVer, tc.blob, CapabilityMlowCodecV1); got != tc.want {
				t.Errorf("CapabilityBitAt(%x) = %v, want %v", tc.blob, got, tc.want)
			}
		})
	}
}

// A byte index past the mask aliases onto a low one the same way the peer that
// built the blob reads it, rather than reading out of range.
func TestCapabilityBitAtAliasesHighIndex(t *testing.T) {
	if got := CapabilityBitAt(1, true, CapabilityOffer, 8*32+31); got != CapabilitySet {
		t.Errorf("index 287 = %v, want it to alias onto index 31 (Set)", got)
	}
}

func TestMlowAfterPeerCapability(t *testing.T) {
	cases := []struct {
		local bool
		peer  CapabilityBit
		want  bool
	}{
		{true, CapabilitySet, true},
		// A peer that announced a readable blob without the index refuses MLow, and
		// the refusal drops it for BOTH directions.
		{true, CapabilityClear, false},
		// A peer that sent no blob is skipped, not treated as a refusal.
		{true, CapabilityUnknown, true},
		{false, CapabilitySet, false},
		{false, CapabilityUnknown, false},
	}
	for _, tc := range cases {
		if got := MlowAfterPeerCapability(tc.local, tc.peer); got != tc.want {
			t.Errorf("MlowAfterPeerCapability(%v, %v) = %v, want %v", tc.local, tc.peer, got, tc.want)
		}
	}
}

func TestWithoutMlowCapabilityClearsOnlyBit31(t *testing.T) {
	got := WithoutMlowCapability(CapabilityOffer)
	want := []byte{0x01, 0x05, 0xf7, 0x09, 0xe0, 0x3b, 0x13}
	if !bytes.Equal(got, want) {
		t.Errorf("WithoutMlowCapability = %x, want %x", got, want)
	}
	if bytes.Equal(got, CapabilityOffer) {
		t.Error("WithoutMlowCapability must not mutate or return the source blob")
	}
	if CapabilityOffer[5] != 0xbb {
		t.Errorf("CapabilityOffer was mutated in place: byte 5 = %#x", CapabilityOffer[5])
	}
}

// An MLow answer sends the plain capability and no <voip_settings>; a
// standard-Opus answer must state both, or the peer keeps decoding our packets
// as MLow and the call is silent in that direction.
func TestAcceptStatesTheCodecSelection(t *testing.T) {
	peer, creator := peerJID(), creatorJID()
	build := func(capability, voipSettings []byte) waBinary.Node {
		return BuildAccept(&AcceptParams{
			CallID: "CID", To: peer, CallCreator: creator,
			AudioRates: []string{"16000"}, Capability: capability, VoipSettings: voipSettings,
		})
	}

	mlow := build(CapabilityOffer, nil)
	action := contentNodes(t, mlow)[0]
	cap, ok := getChild(t, action, "capability")
	if !ok {
		t.Fatal("mlow accept must carry <capability>: a missing blob reads as unstated")
	}
	if got, _ := cap.Content.([]byte); !bytes.Equal(got, CapabilityOffer) {
		t.Errorf("mlow accept capability = %x, want %x", got, CapabilityOffer)
	}
	if _, has := getChild(t, action, "voip_settings"); has {
		t.Error("mlow accept must not carry <voip_settings>: it is the peer's default already")
	}

	opus := build(CapabilityStandardOpusOffer, StandardOpusVoipSettings(false))
	action = contentNodes(t, opus)[0]
	cap, ok = getChild(t, action, "capability")
	if !ok {
		t.Fatal("standard-opus accept must carry <capability>")
	}
	if got, _ := cap.Content.([]byte); CapabilityBitAt(1, true, got, CapabilityMlowCodecV1) != CapabilityClear {
		t.Error("standard-opus accept capability must clear index 31")
	}
	vs, ok := getChild(t, action, "voip_settings")
	if !ok {
		t.Fatal("standard-opus accept must carry <voip_settings>: it selects the peer's DECODER")
	}
	body, _ := vs.Content.([]byte)
	parsed, err := ParseVoipSettings(body, zerolog.Nop())
	if err != nil {
		t.Fatalf("our own voip_settings must parse: %v", err)
	}
	if parsed.UseMlowCodecV1 {
		t.Error("standard-opus voip_settings must set use_mlow_codec_v1=false")
	}
}
