package rtp

import "encoding/binary"

const (
	// Rfc8285OneByteExtensionProfile is RFC 8285's one-byte header extension profile; WhatsApp's
	// stack reads its one-byte elements under this profile as well as under 0xdebe.
	Rfc8285OneByteExtensionProfile uint16 = 0xbede
	// WhatsappRtpExtensionAudioPiggybackID is the one-byte extension element naming where a
	// carried audio packet starts in the payload.
	WhatsappRtpExtensionAudioPiggybackID uint8 = 7
)

// AudioPiggybackOffset reads the payload offset at which a piggybacked audio packet starts, as
// the packet's header states it. ok is false when the packet has no audio piggyback element. The
// header is in the clear, so this can be read before decryption, but the offset only means
// something for an authenticated packet.
func AudioPiggybackOffset(packet []byte) (int, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/rtp.rs#L351-L377
	profile, extension, ok := RtpExtensionProfileAndData(packet)
	if !ok || (profile != WhatsappRtpExtensionProfile && profile != Rfc8285OneByteExtensionProfile) {
		return 0, false
	}
	for len(extension) > 0 {
		header := extension[0]
		extension = extension[1:]
		id := header >> 4
		if id == 0 {
			continue
		}
		if id == 15 {
			break
		}
		length := int(header&0x0f) + 1
		if len(extension) < length {
			break
		}
		value := extension[:length]
		extension = extension[length:]
		if id == WhatsappRtpExtensionAudioPiggybackID && length == 2 {
			return int(binary.BigEndian.Uint16(value)), true
		}
	}
	return 0, false
}

// SplitAudioPiggyback splits a piggybacked audio packet off the decrypted payload of packet. It
// returns the packet's own media and the audio packet (whole, with its header and no tag). When
// the offset is zero, or leaves no audio behind it, the payload comes back unchanged with a nil
// audio. RTP padding, when the outer packet has the P bit, belongs to the media part and is
// removed from its end.
func SplitAudioPiggyback(packet, payload []byte) (media, audio []byte) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/rtp.rs#L385-L399
	offset, ok := AudioPiggybackOffset(packet)
	if !ok || offset == 0 || offset >= len(payload) {
		return payload, nil
	}
	media, audio = payload[:offset], payload[offset:]
	if len(packet) > 0 && packet[0]&0x20 != 0 && len(media) > 0 {
		padding := int(media[len(media)-1])
		if padding <= len(media) {
			media = media[:len(media)-padding]
		}
	}
	return media, audio
}

// PlainRtpPayload is the payload of a plain (unprotected) RTP packet: after the fixed header,
// CSRCs and extension block, and without RTP padding. ok is false for a packet with no payload
// left.
func PlainRtpPayload(packet []byte) ([]byte, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/792923bd62f224078004ea078d640418fe278c00/wacore/src/voip/rtp.rs#L422-L430
	headerLen, ok := RtpHeaderByteLength(packet)
	if !ok {
		return nil, false
	}
	payload := packet[headerLen:]
	if packet[0]&0x20 != 0 {
		if len(payload) == 0 {
			return nil, false
		}
		padding := int(payload[len(payload)-1])
		if padding > len(payload) {
			return nil, false
		}
		payload = payload[:len(payload)-padding]
	}
	return payload, len(payload) > 0
}
