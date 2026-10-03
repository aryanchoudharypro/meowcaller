package meowcaller

import (
	"errors"

	"github.com/purpshell/meowcaller/signaling"
	"go.mau.fi/whatsmeow/types"
)

// groupCallMaxParticipants is the roster ceiling, this client included.
const groupCallMaxParticipants = 32

// hostedDeviceID is the device slot WhatsApp reserves for Cloud API companions.
const hostedDeviceID = 99

// dropHostedDevices removes Cloud API (hosted) companions, which cannot take
// part in a call and must not be offered one.
func dropHostedDevices(devices []types.JID) []types.JID {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d9f78b806f1f4ca80c8008caa5846e5d542c2c55/src/voip/facade.rs#L1227-L1230
	kept := make([]types.JID, 0, len(devices))
	for _, device := range devices {
		if device.Device == hostedDeviceID ||
			device.Server == types.HostedServer || device.Server == types.HostedLIDServer {
			continue
		}
		kept = append(kept, device)
	}
	return kept
}

// groupInviteTargetMatches reports whether participant is the user behind
// either identity of an invite target: the LID it resolved to or the JID the
// caller asked for.
func groupInviteTargetMatches(participant groupCallParticipant, target, requested types.JID) bool {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d9f78b806f1f4ca80c8008caa5846e5d542c2c55/src/voip/facade.rs#L3500-L3516
	matches := func(candidate types.JID) bool {
		if candidate.IsEmpty() {
			return false
		}
		for _, identity := range [2]types.JID{target, requested} {
			if !identity.IsEmpty() && candidate.User == identity.User && candidate.Server == identity.Server {
				return true
			}
		}
		return false
	}
	if matches(participant.JID) || matches(participant.PN) {
		return true
	}
	for _, device := range participant.Devices {
		if matches(device.JID) {
			return true
		}
	}
	return false
}

// groupInviteOfferRoster builds the roster an active-call invitation carries:
// the participants currently connected, never the ones still ringing or gone.
// existingOnly rings a roster member who is not connected; otherwise the
// target must be new to the call.
func groupInviteOfferRoster(
	snapshot groupCallUpdate,
	target, requested types.JID,
	existingOnly bool,
) ([]signaling.GroupCallParticipant, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d9f78b806f1f4ca80c8008caa5846e5d542c2c55/src/voip/facade.rs#L3480-L3563
	var member *groupCallParticipant
	connected := 0
	for i := range snapshot.Participants {
		participant := &snapshot.Participants[i]
		if member == nil && groupInviteTargetMatches(*participant, target, requested) {
			member = participant
		}
		if participant.State == "connected" {
			connected++
		}
	}
	if !existingOnly && len(snapshot.Participants) >= groupCallMaxParticipants {
		return nil, errors.New("meowcaller: group participant limit reached")
	}
	if snapshot.ConnectedLimit != 0 && connected >= int(snapshot.ConnectedLimit) {
		return nil, errors.New("meowcaller: group connected-participant limit reached")
	}
	if existingOnly {
		if member == nil {
			return nil, errors.New("meowcaller: participant is not in the call roster")
		}
		if member.State == "connected" {
			return nil, errors.New("meowcaller: participant is already connected")
		}
	} else if member != nil {
		return nil, errors.New("meowcaller: participant is already in the call roster")
	}
	filtered := groupCallUpdate{Participants: make([]groupCallParticipant, 0, connected)}
	for _, participant := range snapshot.Participants {
		if participant.State != "connected" || groupInviteTargetMatches(participant, target, requested) {
			continue
		}
		filtered.Participants = append(filtered.Participants, participant)
	}
	if len(filtered.Participants) == 0 {
		return nil, errors.New("meowcaller: group call has no connected participant roster")
	}
	return signalingParticipantsFromUpdate(filtered), nil
}

// groupInviteContext resolves what an invitation for target has to carry: the
// connected roster of an established group call, or the two active devices of
// a 1:1 call that the invitation is about to promote.
func (e *engine) groupInviteContext(
	callID string,
	target, requested types.JID,
	existingOnly bool,
) (types.JID, []signaling.GroupCallParticipant, bool, error) {
	creator, participants, video, err := e.groupInviteRoster(callID)
	if err != nil {
		return types.EmptyJID, nil, false, err
	}
	e.mu.Lock()
	var snapshot *groupCallUpdate
	if m := e.calls[callID]; m != nil && m.groupUpdate != nil {
		cloned := cloneGroupCallUpdate(*m.groupUpdate)
		snapshot = &cloned
	}
	e.mu.Unlock()
	if snapshot == nil {
		if existingOnly {
			return types.EmptyJID, nil, false, errors.New("meowcaller: participant is not in the call roster")
		}
		for _, participant := range participants {
			if participant.JID.ToNonAD() == target.ToNonAD() {
				return types.EmptyJID, nil, false, errors.New("meowcaller: participant is already in the call roster")
			}
		}
		return creator, participants, video, nil
	}
	participants, err = groupInviteOfferRoster(*snapshot, target, requested, existingOnly)
	if err != nil {
		return types.EmptyJID, nil, false, err
	}
	if snapshot.Media != "" {
		// The server's roster says what the call is; local flags can lag it.
		video = snapshot.Media == "video"
	}
	return creator, participants, video, nil
}
