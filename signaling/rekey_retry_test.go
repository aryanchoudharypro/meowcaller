package signaling

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func TestEncRekeyRetryReceiptRoundTrip(t *testing.T) {
	from := types.NewADJID("100000000000001", 0, 3)
	from.Server = types.HiddenUserServer
	creator := types.NewJID("100000000000002", types.HiddenUserServer)
	node, err := BuildEncRekeyRetryReceipt(EncRekeyRetry{
		From: from, ID: "3EB0AABBCCDD", CallID: "0123456789ABCDEF0123456789ABCDEF",
		CallCreator: creator, Count: 2, RegistrationID: 0x01020304,
	})
	if err != nil {
		t.Fatal(err)
	}
	if node.Tag != "receipt" || node.Attrs["type"] != "enc_rekey_retry" || node.Attrs["id"] != "3EB0AABBCCDD" {
		t.Fatalf("unexpected receipt %v", node.Attrs)
	}
	reg, _ := node.GetOptionalChildByTag("registration")
	if got := reg.Content.([]byte); len(got) != 4 || got[0] != 1 || got[3] != 4 {
		t.Fatalf("registration %x, want 01020304 big-endian", got)
	}

	// What the requester's device sends is what we receive, with its from.
	incoming := node
	incoming.Attrs = waBinary.Attrs{"from": from, "id": node.Attrs["id"], "type": node.Attrs["type"]}
	retry, err := ParseEncRekeyRetryReceipt(&incoming)
	if err != nil {
		t.Fatal(err)
	}
	if retry.From != from || retry.CallID != "0123456789ABCDEF0123456789ABCDEF" || retry.CallCreator != creator ||
		retry.Count != 2 || retry.RegistrationID != 0x01020304 || retry.ID != "3EB0AABBCCDD" {
		t.Fatalf("parsed %+v", retry)
	}
}

func TestGroupEncRekeyCarriesRetryCount(t *testing.T) {
	to := types.NewADJID("100000000000001", 0, 3)
	to.Server = types.HiddenUserServer
	node, err := BuildGroupEncRekey(GroupEncRekeyParams{
		CallID: "0123456789ABCDEF0123456789ABCDEF", To: to,
		CallCreator: types.NewJID("100000000000002", types.HiddenUserServer), TransactionID: 7, RequestID: "1",
		DeviceKey:  OfferDeviceKey{DeviceJid: to, Ciphertext: []byte{1}, EncType: "msg"},
		RetryCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	action := node.GetChildren()[0]
	enc, _ := action.GetOptionalChildByTag("enc")
	if enc.Attrs["count"] != "3" {
		t.Fatalf("enc count %v, want 3", enc.Attrs["count"])
	}
}
