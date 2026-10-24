package signaling

import (
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// BuildMuteRequest asks one group-call participant to mute: a <mute_v2> that
// carries request-state instead of mute-state, sent to that participant.
func BuildMuteRequest(callID string, to, creator types.JID, requestID string) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	return callWrap(to, &requestID, waBinary.Node{
		Tag:   "mute_v2",
		Attrs: waBinary.Attrs{"call-id": callID, "call-creator": creator, "request-state": "1"},
	})
}

// BuildRemoveUser asks the call server to remove one participant from a group
// call this account administers.
func BuildRemoveUser(callID string, creator, user types.JID, requestID string) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/wasm.lock.json#L1-L4
	return callWrap(types.NewJID(callID, "call"), &requestID, waBinary.Node{
		Tag:   "remove_user",
		Attrs: waBinary.Attrs{"call-id": callID, "call-creator": creator, "jid": user},
	})
}
