package meowcaller

import (
	"testing"

	"github.com/purpshell/meowcaller/rtp"
)

// Audio keeps authenticating and decrypting across the 16-bit sequence wrap,
// where the SRTP rollover counter moves (an hour into a 60 ms call).
func TestAudioSurvivesTheSequenceWrap(t *testing.T) {
	old := rtp.InitialSequence
	rtp.InitialSequence = 65530
	defer func() { rtp.InitialSequence = old }()

	callKey := make([]byte, 32)
	for i := range callKey {
		callKey[i] = byte(i + 3)
	}
	self, peer := mediaTestJID("100", 1), mediaTestJID("200", 2)
	registry, err := newParticipantReceiveRegistry("CID", callKey, self.String(), peer.String(),
		func() participantAudioDecoder { return &recordingParticipantDecoder{} })
	if err != nil {
		t.Fatal(err)
	}
	ssrc, err := rtp.DeriveWasmParticipantSsrc("CID", rtp.FormatE2ESrtpParticipantID(peer.String()), 0)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := NewMediaPipeline(callKey, peer.String(), self.String(), ssrc, FrameSamples)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		packet, err := sender.ProtectAudio([]byte{0x10, byte(i), 1, 2, 3})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := registry.DecodeAudio(packet); !ok {
			t.Fatalf("packet %d (sequence %d) did not authenticate", i, uint16(65530+i))
		}
	}
}
