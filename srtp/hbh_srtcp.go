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
// The halves: copy_relay_info (wasm function 13657) keeps the decoded 30-byte
// <hbh_key> as a 14-byte master salt (bytes 0..14) followed by a 16-byte master
// key (bytes 14..30), and derive_hbh_srtp_key (11216, via wa_sfu_kdf 11245)
// runs HKDF(salt 32 zero bytes, ikm master salt, info "<x> salt") to 32 bytes,
// then HKDF(that, ikm master key, info "<x> key") to 30. whatsapp-rust splits
// the key 16/14 instead (srtp/hbh.go, pinned by its KAT); no relay packet
// ever authenticated under that split (3143 rejected in one tester's logs,
// 2026-11). NOT VALIDATED against a live relay: the WA split is tried first,
// the earlier guesses after it; the caller keeps the first that authenticates.

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
	// Which bytes feed the salt step and which the key step, best first.
	splits := []struct {
		suffix    string
		salt, key []byte
	}{
		{"", hbhKey[0:14], hbhKey[14:30]},
		{"/rust", hbhKey[16:30], hbhKey[0:16]},
		{"/swapped", hbhKey[0:16], hbhKey[16:30]},
	}
	for _, split := range splits {
		for _, labels := range hbhSrtcpLabels {
			salt, err := util.HKDFSHA256(make([]byte, 32), split.salt, []byte(labels.salt), 32)
			if err != nil {
				continue
			}
			master, err := util.HKDFSHA256(salt, split.key, []byte(labels.key), 30)
			if err != nil {
				continue
			}
			keys, err := deriveSessionKeysFromMasterLabels(master, 0x03, 0x04, 0x05)
			if err != nil {
				continue
			}
			out = append(out, HbhSrtcpVariant{Name: labels.name + split.suffix, Keys: keys})
		}
	}
	return out
}
