package signaling

import (
	"encoding/binary"
	"fmt"
	"strconv"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// EncRekeyRetryReceiptType is the receipt type a group call participant sends
// when it couldn't decrypt an enc_rekey (ENC_RETRY_RECEIPT_ATTRS.GROUP_CALL).
const EncRekeyRetryReceiptType = "enc_rekey_retry"

// EncRekeyRetry asks the sender of an enc_rekey to send its key epoch again.
type EncRekeyRetry struct {
	// From is the device that couldn't decrypt the epoch.
	From types.JID
	// ID is the id of the enc_rekey stanza that failed.
	ID          string
	CallID      string
	CallCreator types.JID
	// Count is the retry attempt, from 1.
	Count int
	// RegistrationID is the requester's Signal registration id.
	RegistrationID uint32
}

// BuildEncRekeyRetryReceipt builds the receipt that asks a device to resend
// the enc_rekey whose stanza id is retry.ID.
func BuildEncRekeyRetryReceipt(retry EncRekeyRetry) (waBinary.Node, error) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/stanza/index.json#L4345-L4409
	if retry.From.IsEmpty() || retry.ID == "" || retry.CallID == "" || retry.CallCreator.IsEmpty() || retry.Count < 1 {
		return waBinary.Node{}, fmt.Errorf("signaling: build enc_rekey retry: incomplete identity")
	}
	var registration [4]byte
	binary.BigEndian.PutUint32(registration[:], retry.RegistrationID)
	return waBinary.Node{
		Tag:   "receipt",
		Attrs: waBinary.Attrs{"to": retry.From, "id": retry.ID, "type": EncRekeyRetryReceiptType},
		Content: []waBinary.Node{
			{Tag: "enc_rekey", Attrs: waBinary.Attrs{
				"call-creator": retry.CallCreator, "call-id": retry.CallID,
				"count": strconv.Itoa(retry.Count),
			}},
			{Tag: "registration", Content: registration[:]},
		},
	}, nil
}

// ParseEncRekeyRetryReceipt parses an incoming enc_rekey_retry receipt. From
// is the device that sent it.
func ParseEncRekeyRetryReceipt(node *waBinary.Node) (*EncRekeyRetry, error) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/stanza/index.json#L4345-L4409
	if node == nil || node.Tag != "receipt" || node.AttrGetter().OptionalString("type") != EncRekeyRetryReceiptType {
		return nil, fmt.Errorf("signaling: parse enc_rekey retry: unexpected node")
	}
	ag := node.AttrGetter()
	retry := &EncRekeyRetry{ID: ag.String("id")}
	retry.From = ag.OptionalJIDOrEmpty("participant")
	if retry.From.IsEmpty() {
		retry.From = ag.JID("from")
	}
	if !ag.OK() {
		return nil, fmt.Errorf("signaling: parse enc_rekey retry: %w", ag.Error())
	}
	rekey, ok := node.GetOptionalChildByTag("enc_rekey")
	if !ok {
		return nil, fmt.Errorf("signaling: parse enc_rekey retry: no enc_rekey")
	}
	rag := rekey.AttrGetter()
	retry.CallID = rag.String("call-id")
	retry.CallCreator = rag.OptionalJIDOrEmpty("call-creator")
	retry.Count = rag.Int("count")
	if !rag.OK() {
		return nil, fmt.Errorf("signaling: parse enc_rekey retry: %w", rag.Error())
	}
	registration, ok := node.GetOptionalChildByTag("registration")
	if !ok {
		return nil, fmt.Errorf("signaling: parse enc_rekey retry: no registration")
	}
	regBytes, _ := registration.Content.([]byte)
	if len(regBytes) == 0 || len(regBytes) > 4 {
		return nil, fmt.Errorf("signaling: parse enc_rekey retry: registration is %d bytes", len(regBytes))
	}
	for _, b := range regBytes {
		retry.RegistrationID = retry.RegistrationID<<8 | uint32(b)
	}
	return retry, nil
}
