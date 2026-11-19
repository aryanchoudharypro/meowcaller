package srtp

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/purpshell/meowcaller/util"
)

func TestHbhSrtcpReceiveCandidatesAreDistinctAndRoundTrip(t *testing.T) {
	hbhKey := make([]byte, 30)
	for i := range hbhKey {
		hbhKey[i] = byte(i*7 + 3)
	}
	candidates := HbhSrtcpReceiveCandidates(hbhKey)
	if len(candidates) != 9 {
		t.Fatalf("%d candidates, want 3 label pairs x 3 splits", len(candidates))
	}
	seen := map[[16]byte]string{}
	for _, c := range candidates {
		if other, dup := seen[c.Keys.CipherKey]; dup {
			t.Fatalf("%s and %s derived the same key", c.Name, other)
		}
		seen[c.Keys.CipherKey] = c.Name
	}
	if candidates[0].Name != "downlink" {
		t.Fatalf("first candidate %q, want downlink", candidates[0].Name)
	}
	// The first is WA's own split: 14-byte salt, then 16-byte key.
	salt, err := util.HKDFSHA256(make([]byte, 32), hbhKey[0:14], []byte("downlink hbh srtcp salt"), 32)
	if err != nil {
		t.Fatal(err)
	}
	master, err := util.HKDFSHA256(salt, hbhKey[14:30], []byte("downlink hbh srtcp key"), 30)
	if err != nil {
		t.Fatal(err)
	}
	waKeys, err := deriveSessionKeysFromMasterLabels(master, 0x03, 0x04, 0x05)
	if err != nil {
		t.Fatal(err)
	}
	if candidates[0].Keys != waKeys {
		t.Fatal("first candidate differs from WA's 14/16 split")
	}
	// The whatsapp-rust split is still tried.
	want, err := DeriveHbhSrtpKeyDownlink(hbhKey)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys, err := deriveSessionKeysFromMasterLabels(want, 0x03, 0x04, 0x05)
	if err != nil {
		t.Fatal(err)
	}
	if candidates[3].Name != "downlink/rust" || candidates[3].Keys != wantKeys {
		t.Fatalf("candidate 3 %q differs from DeriveHbhSrtpKeyDownlink", candidates[3].Name)
	}
	// A NACK protected under one keying opens under that keying only.
	nack := []byte{0x81, 205, 0, 3, 0, 0, 0, 1, 0x12, 0x34, 0x56, 0x78, 0, 100, 0, 5}
	packet, err := ProtectSrtcp(&candidates[3].Keys, 1, 7, nack)
	if err != nil {
		t.Fatal(err)
	}
	if len(packet) != 30 {
		t.Fatalf("protected NACK is %d bytes; the relay's unexplained packets were 30", len(packet))
	}
	for i, c := range candidates {
		plain, _, ok := UnprotectSrtcp(&c.Keys, 1, packet)
		if ok != (i == 3) {
			t.Fatalf("candidate %s authenticated=%v", c.Name, ok)
		}
		if ok && !bytes.Equal(plain, nack) {
			t.Fatal("round trip changed the packet")
		}
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(hbhKey))
	if len(encoded) != 40 {
		t.Fatalf("base64 of the key is %d bytes", len(encoded))
	}
	fromBase64 := HbhSrtcpReceiveCandidates(encoded)
	if len(fromBase64) != len(candidates) || fromBase64[0].Keys != candidates[0].Keys {
		t.Fatal("the 40-character base64 <hbh_key> the relay sends did not give the same keys")
	}
	if HbhSrtcpReceiveCandidates(make([]byte, 16)) != nil {
		t.Fatal("a key of the wrong length produced candidates")
	}
}
