package rtp

import (
	"bytes"
	"testing"
)

// piggybackCarrier is a video packet whose 0xdebe extension names where its own media ends and
// the carried audio packet starts.
func piggybackCarrier(media, audio []byte, padded bool) []byte {
	header := []byte{0x90, RtpPayloadTypeH264, 0, 7, 0, 0, 0x23, 0x28, 0, 0, 0, 5}
	if padded {
		header[0] |= 0x20
	}
	header = append(header, 0xde, 0xbe, 0, 1, 0x71, 0, byte(len(media)), 0)
	out := append(header, media...)
	return append(out, audio...)
}

func TestAudioPiggybackOffsetReadsTheExtensionElement(t *testing.T) {
	media := []byte{0x41, 1, 2, 3}
	audio := []byte{0x80, 120, 0, 9, 0, 0, 0, 1, 0, 0, 0, 2, 0xf8, 0xff}
	carrier := piggybackCarrier(media, audio, false)
	offset, ok := AudioPiggybackOffset(carrier)
	if !ok || offset != len(media) {
		t.Fatalf("offset %d ok %v", offset, ok)
	}
	if _, ok := AudioPiggybackOffset(fecTestPacket(1, []byte{1, 2, 3})); ok {
		t.Fatal("a packet without an extension has a piggyback")
	}
}

func TestSplitAudioPiggybackReturnsTheMediaAndTheWholeAudioPacket(t *testing.T) {
	media := []byte{0x41, 1, 2, 3}
	audio := []byte{0x80, 120, 0, 9, 0, 0, 0, 1, 0, 0, 0, 2, 0xf8, 0xff}
	carrier := piggybackCarrier(media, audio, false)
	gotMedia, gotAudio := SplitAudioPiggyback(carrier, append(append([]byte(nil), media...), audio...))
	if !bytes.Equal(gotMedia, media) || !bytes.Equal(gotAudio, audio) {
		t.Fatalf("media %x audio %x", gotMedia, gotAudio)
	}
}

func TestSplitAudioPiggybackTakesRtpPaddingOffTheMedia(t *testing.T) {
	media := []byte{0x41, 1, 0, 0, 2}
	audio := []byte{0x80, 120, 0, 9, 0, 0, 0, 1, 0, 0, 0, 2, 0xf8}
	carrier := piggybackCarrier(media, audio, true)
	gotMedia, gotAudio := SplitAudioPiggyback(carrier, append(append([]byte(nil), media...), audio...))
	if !bytes.Equal(gotMedia, []byte{0x41, 1, 0}) || !bytes.Equal(gotAudio, audio) {
		t.Fatalf("media %x audio %x", gotMedia, gotAudio)
	}
}

func TestSplitAudioPiggybackLeavesAPayloadWithoutAudioAlone(t *testing.T) {
	media := []byte{0x41, 1, 2, 3}
	carrier := piggybackCarrier(media, nil, false)
	gotMedia, gotAudio := SplitAudioPiggyback(carrier, media)
	if !bytes.Equal(gotMedia, media) || gotAudio != nil {
		t.Fatalf("media %x audio %x", gotMedia, gotAudio)
	}
}

func TestPlainRtpPayloadStopsBeforePadding(t *testing.T) {
	packet := fecTestPacket(1, []byte{5, 6, 7, 0, 2})
	packet[0] |= 0x20
	payload, ok := PlainRtpPayload(packet)
	if !ok || !bytes.Equal(payload, []byte{5, 6, 7}) {
		t.Fatalf("payload %x ok %v", payload, ok)
	}
	if _, ok := PlainRtpPayload(fecTestPacket(1, nil)); ok {
		t.Fatal("an empty payload was returned")
	}
}
