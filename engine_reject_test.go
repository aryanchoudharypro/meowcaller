package meowcaller

import (
	"testing"

	"github.com/purpshell/meowcaller/signaling"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func rejectFrom(device uint16, reason string) *events.CallReject {
	from := peerJID()
	from.Device = device
	node := &waBinary.Node{Tag: "reject", Attrs: waBinary.Attrs{"call-id": "CID"}}
	if reason != "" {
		node.Attrs["reason"] = reason
	}
	return &events.CallReject{BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: from}, Data: node}
}

func TestRejectFromOneDeviceKeepsOutgoingCallRinging(t *testing.T) {
	cases := []struct {
		name   string
		device uint16
		reason string
	}{
		{"linked device busy", 17, signaling.RejectReasonBusy},
		{"linked device enc", 17, signaling.RejectReasonEnc},
		{"phone enc", 0, signaling.RejectReasonEnc},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, call := testEngineWithOutgoingCall()
			eng.onReject(rejectFrom(tc.device, tc.reason))
			if got := call.State(); got == CallPhaseEnded {
				t.Fatalf("call ended on a reject that speaks for one device only")
			}
		})
	}
}

func TestRejectThatDeclinesEndsOutgoingCall(t *testing.T) {
	cases := []struct {
		name   string
		device uint16
		reason string
		want   string
	}{
		{"linked device declines", 17, "", "rejected"},
		{"phone declines", 0, "", "rejected"},
		{"phone busy", 0, signaling.RejectReasonBusy, "busy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, call := testEngineWithOutgoingCall()
			var reason string
			call.OnEnd(func(r string) { reason = r })
			eng.onReject(rejectFrom(tc.device, tc.reason))
			if got := call.State(); got != CallPhaseEnded {
				t.Fatalf("phase = %d, want Ended", got)
			}
			if reason != tc.want {
				t.Fatalf("reason = %q, want %q", reason, tc.want)
			}
		})
	}
}

func TestBuildRejectWithReason(t *testing.T) {
	node := signaling.BuildRejectWithReason("CID", peerJID(), creatorJID(), signaling.RejectReasonBusy)
	action := node.GetChildren()[0]
	if action.Tag != "reject" || action.Attrs["reason"] != "busy" {
		t.Fatalf("reject = %v, want reason=busy", action)
	}
	plainNode := signaling.BuildReject("CID", peerJID(), creatorJID())
	plain := plainNode.GetChildren()[0]
	if _, ok := plain.Attrs["reason"]; ok {
		t.Fatalf("plain reject carries a reason: %v", plain)
	}
}
