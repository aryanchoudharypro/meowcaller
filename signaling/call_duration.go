package signaling

import (
	"strconv"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// DurationParams are the inputs to BuildDuration.
type DurationParams struct {
	CallID       string
	CallCreator  types.JID
	Peer         types.JID
	Type         string // "1x1", "genai" or "group"
	AudioSeconds uint32
	VideoSeconds uint32
}

// BuildDuration builds the <duration> stats stanza sent to the call server
// when a connected call ends.
func BuildDuration(p *DurationParams, requestID string) waBinary.Node {
	// Source of truth: wasm serialize_duration (func 14127, stats.cc) and make_and_send_duration_msg (func 12623, call_signaling_sender.cc): target "call", type 1x1/genai, peer, audio_duration, video_duration.
	attrs := waBinary.Attrs{
		"call-id": p.CallID, "call-creator": p.CallCreator, "type": p.Type,
		"audio_duration": strconv.FormatUint(uint64(p.AudioSeconds), 10),
		"video_duration": strconv.FormatUint(uint64(p.VideoSeconds), 10),
	}
	if !p.Peer.IsEmpty() {
		attrs["peer"] = p.Peer
	}
	return waBinary.Node{
		Tag: "call", Attrs: waBinary.Attrs{"to": types.NewJID("", "call"), "id": requestID},
		Content: []waBinary.Node{{Tag: "duration", Attrs: attrs}},
	}
}
