package srtp

import (
	"encoding/base64"

	"github.com/purpshell/meowcaller/util"
)

// The relay speaks RTCP of its own to each client (keyframe requests, NACKs and
// bandwidth feedback for the client's uplink), protected hop by hop under keys
// derived from the relay block's <hbh_key>, not under the call's e2e keys.
//
// WA Web's derive_hbh_srtp_key (voip wasm function 11666 in the 2026-11 build)
// runs two HKDF-SHA256 steps over the two halves of the hop-by-hop key, with
// labels picked by index from a ten-entry table (wasm memory 0x152ca4):
//
//	2/3 "hbh srtcp salt" / "hbh srtcp key"
//	4/5 "hbh srtp salt" / "hbh srtp key"
//	6/7 "uplink hbh srtcp salt" / "uplink hbh srtcp key"
//	8/9 "downlink hbh srtcp salt" / "downlink hbh srtcp key"
//
// For a 1:1 call it derives three SRTCP keys: 2/3, 6/7 and 8/9. Each 30-byte
// result is a libsrtp AES_CM_128_HMAC_SHA1_80 master key and salt.
//
// NOT VALIDATED against a live relay: which of the three the relay protects
// its packets with, and which half of the hop-by-hop key feeds which HKDF step,
// are not settled by the wasm read so far. HbhSrtcpReceiveCandidates returns
// every combination; the caller keeps the first one that authenticates.

// HbhSrtcpVariant is one candidate keying for the relay's RTCP.
type HbhSrtcpVariant struct {
	// Name says which labels and half order produced Keys, for the log.
	Name string
	Keys E2eSrtpKeys
}

var hbhSrtcpLabels = []struct{ name, salt, key string }{
	{"downlink", "downlink hbh srtcp salt", "downlink hbh srtcp key"},
	{"plain", "hbh srtcp salt", "hbh srtcp key"},
	{"uplink", "uplink hbh srtcp salt", "uplink hbh srtcp key"},
}

// HbhKeyBytes returns the 30-byte hop-by-hop key from an <hbh_key> node's
// content: the relay sends it as 40 characters of base64 (seen live
// 2026-11-14), which is exactly 30 bytes; raw 30-byte content is taken as is.
// It returns nil for anything else.
func HbhKeyBytes(content []byte) []byte {
	if len(content) == 30 {
		return content
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if raw, err := enc.DecodeString(string(content)); err == nil && len(raw) == 30 {
			return raw
		}
	}
	return nil
}

// HbhSrtcpReceiveCandidates derives every candidate keying for the relay's
// hop-by-hop RTCP from an <hbh_key> (raw 30 bytes or its base64). It returns
// nil for anything else.
func HbhSrtcpReceiveCandidates(hbhKey []byte) []HbhSrtcpVariant {
	hbhKey = HbhKeyBytes(hbhKey)
	if hbhKey == nil {
		return nil
	}
	var out []HbhSrtcpVariant
	for _, labels := range hbhSrtcpLabels {
		for _, swapped := range []bool{false, true} {
			// whatsapp-rust's order: salt step over the 14-byte master salt,
			// key step over the 16-byte master key.
			first, second := hbhKey[16:30], hbhKey[0:16]
			name := labels.name
			if swapped {
				first, second = second, first
				name += "/swapped"
			}
			salt, err := util.HKDFSHA256(make([]byte, 32), first, []byte(labels.salt), 32)
			if err != nil {
				continue
			}
			master, err := util.HKDFSHA256(salt, second, []byte(labels.key), 30)
			if err != nil {
				continue
			}
			keys, err := deriveSessionKeysFromMasterLabels(master, 0x03, 0x04, 0x05)
			if err != nil {
				continue
			}
			out = append(out, HbhSrtcpVariant{Name: name, Keys: keys})
		}
	}
	return out
}
