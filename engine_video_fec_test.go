package meowcaller

import (
	"bytes"
	"testing"

	"github.com/purpshell/meowcaller/rtp"
)

func newOneToOneRegistryForFec(t *testing.T) (*participantReceiveRegistry, []byte, string, string, uint32, *recordingParticipantDecoder) {
	t.Helper()
	callKey := iota32()
	self := mediaTestJID("111111111111111", 14)
	peer := mediaTestJID("222222222222222", 0)
	decoder := &recordingParticipantDecoder{}
	registry, err := newParticipantReceiveRegistry("CID", callKey, self.String(), peer.String(), func() participantAudioDecoder {
		return decoder
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	peerID := rtp.FormatE2ESrtpParticipantID(peer.String())
	peerSSRC, err := rtp.DeriveWasmParticipantSsrc("CID", peerID, 0)
	if err != nil {
		t.Fatalf("derive peer SSRC: %v", err)
	}
	return registry, callKey, self.String(), peer.String(), peerSSRC, decoder
}

func TestUnprotectAuxiliaryReadsTheRepairStreamUnderTheCallKeys(t *testing.T) {
	registry, callKey, self, peer, _, _ := newOneToOneRegistryForFec(t)
	const repairSSRC, otherSSRC uint32 = 0x0badf00d, 0x0ddba11
	repairTx, err := NewMediaPipeline(callKey, peer, self, repairSSRC, FrameSamples)
	if err != nil {
		t.Fatalf("repair sender: %v", err)
	}
	otherTx, err := NewMediaPipeline(callKey, peer, self, otherSSRC, FrameSamples)
	if err != nil {
		t.Fatalf("other sender: %v", err)
	}
	for sequence := uint16(65530); sequence != 6; sequence++ {
		for _, tx := range []struct {
			pipe *MediaPipeline
			ssrc uint32
		}{{repairTx, repairSSRC}, {otherTx, otherSSRC}} {
			payload := []byte{byte(sequence), 1, 2, 3}
			packet, err := tx.pipe.ProtectRTP(&rtp.RtpHeader{
				PayloadType: rtp.RtpPayloadTypeVideoFec, SequenceNumber: sequence, Timestamp: 9000, Ssrc: tx.ssrc,
			}, payload)
			if err != nil {
				t.Fatalf("protect: %v", err)
			}
			media, ok := registry.UnprotectAuxiliary(packet)
			if !ok || !bytes.Equal(media.Payload, payload) || media.Header.Ssrc != tx.ssrc {
				t.Fatalf("sequence %d ssrc %x: ok=%v payload=%x", sequence, tx.ssrc, ok, media.Payload)
			}
		}
	}
}

func TestUnprotectAuxiliaryRefusesAForgedPacket(t *testing.T) {
	registry, callKey, self, peer, _, _ := newOneToOneRegistryForFec(t)
	tx, err := NewMediaPipeline(callKey, peer, self, 0x0badf00d, FrameSamples)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	packet, err := tx.ProtectRTP(&rtp.RtpHeader{
		PayloadType: rtp.RtpPayloadTypeVideoFec, SequenceNumber: 1, Timestamp: 9000, Ssrc: 0x0badf00d,
	}, []byte{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("protect: %v", err)
	}
	packet[len(packet)-1] ^= 0xff
	if _, ok := registry.UnprotectAuxiliary(packet); ok {
		t.Fatal("a packet with a bad tag authenticated")
	}
}

func TestDecodeCarriedAudioPlaysAnAudioPacketWithNoSrtpOfItsOwn(t *testing.T) {
	registry, _, _, _, peerSSRC, decoder := newOneToOneRegistryForFec(t)
	header := rtp.RtpHeader{PayloadType: rtp.RtpPayloadTypeOpus, SequenceNumber: 9, Timestamp: 4800, Ssrc: peerSSRC}
	packet := append(rtp.EncodeRtpHeader(&header), 0x07, 0x08)
	audio, ok := registry.DecodeCarriedAudio(packet)
	if !ok || audio.SSRC != peerSSRC || audio.Timestamp != 4800 {
		t.Fatalf("ok=%v audio=%+v", ok, audio)
	}
	if len(decoder.payloads) != 1 || !bytes.Equal(decoder.payloads[0], []byte{0x07, 0x08}) {
		t.Fatalf("decoder saw %x", decoder.payloads)
	}

	stranger := rtp.RtpHeader{PayloadType: rtp.RtpPayloadTypeOpus, SequenceNumber: 9, Timestamp: 4800, Ssrc: peerSSRC + 1}
	if _, ok := registry.DecodeCarriedAudio(append(rtp.EncodeRtpHeader(&stranger), 1)); ok {
		t.Fatal("audio from an SSRC that is not a participant was played")
	}
	video := rtp.RtpHeader{PayloadType: rtp.RtpPayloadTypeH264, SequenceNumber: 9, Timestamp: 4800, Ssrc: peerSSRC}
	if _, ok := registry.DecodeCarriedAudio(append(rtp.EncodeRtpHeader(&video), 1)); ok {
		t.Fatal("a video payload type was played as audio")
	}
	if _, ok := registry.DecodeCarriedAudio(rtp.EncodeRtpHeader(&header)); ok {
		t.Fatal("an audio packet with nothing after its header was played")
	}
}
