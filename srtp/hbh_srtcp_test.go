package srtp

import (
	"bytes"
	"testing"
)

func TestHbhSrtcpReceiveCandidatesAreDistinctAndRoundTrip(t *testing.T) {
	hbhKey := make([]byte, 30)
	for i := range hbhKey {
		hbhKey[i] = byte(i*7 + 3)
	}
	candidates := HbhSrtcpReceiveCandidates(hbhKey)
	if len(candidates) != 6 {
		t.Fatalf("%d candidates, want 3 label pairs x 2 half orders", len(candidates))
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
	// The unswapped labels match the existing whatsapp-rust derivation.
	want, err := DeriveHbhSrtpKeyDownlink(hbhKey)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys, err := deriveSessionKeysFromMasterLabels(want, 0x03, 0x04, 0x05)
	if err != nil {
		t.Fatal(err)
	}
	if candidates[0].Keys != wantKeys {
		t.Fatal("downlink candidate differs from DeriveHbhSrtpKeyDownlink")
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
	if HbhSrtcpReceiveCandidates(make([]byte, 16)) != nil {
		t.Fatal("a key of the wrong length produced candidates")
	}
}
