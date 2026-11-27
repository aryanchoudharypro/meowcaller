package meowcaller

import (
	"context"
	"errors"
	"fmt"

	"github.com/purpshell/meowcaller/signaling"
	"go.mau.fi/whatsmeow/types"
)

func muteStateAttr(muted bool) string {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/wacore/src/stanza/call.rs#L1165-L1180
	if muted {
		return "1"
	}
	return "0"
}

// muteTarget is where this call's mute state is addressed, or ok=false while
// there is no answered call to carry it.
func (e *engine) muteTarget(callID string) (to, creator types.JID, ok bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/src/voip/facade.rs#L3916-L3931
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil {
		e.mu.Unlock()
		return types.EmptyJID, types.EmptyJID, false
	}
	to, creator = m.from, m.creator
	group, direction, answered, call := m.group, m.direction, m.acceptPending || m.acceptSent, m.call
	e.mu.Unlock()
	if to.IsEmpty() {
		return to, creator, false
	}
	switch {
	case group:
		return to, creator, true
	case direction == CallDirectionIncoming:
		return to, creator, answered
	default:
		return to, creator, call != nil && call.isPeerAccepted()
	}
}

// setMuted applies and announces this client's microphone state.
func (e *engine) setMuted(c *Call, muted bool) error {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/src/voip/facade.rs#L3876-L3913
	if c.State() == CallPhaseEnded {
		return errors.New("meowcaller: call is not active")
	}
	c.muteMu.Lock()
	defer c.muteMu.Unlock()
	to, creator, ok := e.muteTarget(c.id)
	if !ok {
		c.setMutedLocal(muted)
		e.c.log.Debug().Str("call_id", c.id).Bool("muted", muted).Msg("mute recorded locally, call not answered yet")
		return nil
	}
	if muted {
		c.setMutedLocal(true)
	}
	if peers := e.connectedGroupPeers(c.id); len(peers) > 0 {
		// Source of truth: WA VoIP wasm wa_system_make_and_send_mute_v2_msg; phones in group calls send mute_v2 to each participant, not to <call-id>@call.
		var firstErr error
		for _, peer := range peers {
			node := signaling.BuildMuteV2(c.id, peer, creator, muteStateAttr(muted))
			node.Attrs["id"] = e.nextCallNodeID()
			if err := e.transmitCallNode(context.Background(), node); err != nil {
				e.c.log.Warn().Err(err).Str("call_id", c.id).Str("to", peer.String()).Msg("send own mute_v2 failed")
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		if firstErr != nil {
			return fmt.Errorf("meowcaller: send mute_v2: %w", firstErr)
		}
		c.setMutedLocal(muted)
		e.c.log.Info().Str("call_id", c.id).Bool("muted", muted).Int("participants", len(peers)).Msg("announced own mute state")
		return nil
	}
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/src/client/voip.rs#L1746-L1770
	node := signaling.BuildMuteV2(c.id, to, creator, muteStateAttr(muted))
	node.Attrs["id"] = e.nextCallNodeID()
	if err := e.transmitCallNode(context.Background(), node); err != nil {
		e.c.log.Warn().Err(err).Str("call_id", c.id).Bool("muted", muted).Msg("send own mute_v2 failed")
		return fmt.Errorf("meowcaller: send mute_v2: %w", err)
	}
	c.setMutedLocal(muted)
	e.c.log.Info().Str("call_id", c.id).Bool("muted", muted).Msg("announced own mute state")
	return nil
}

// sendDTMF sends one keypad tone to the peer of an answered call.
func (e *engine) sendDTMF(c *Call, tone string) error {
	// Source of truth: WA Web VoIP wasm rEjXwUYzMaj wa_call_send_capi_dtmf_tone (func 1159: "Call not active" unless connected)
	to, creator, ok := e.muteTarget(c.id)
	if !ok || c.State() == CallPhaseEnded {
		return errors.New("meowcaller: call is not active")
	}
	node := signaling.BuildDTMF(c.id, to, creator, tone)
	node.Attrs["id"] = e.nextCallNodeID()
	if err := e.transmitCallNode(context.Background(), node); err != nil {
		e.c.log.Warn().Err(err).Str("call_id", c.id).Msg("send dtmf failed")
		return fmt.Errorf("meowcaller: send dtmf: %w", err)
	}
	e.c.log.Debug().Str("call_id", c.id).Msg("sent keypad tone")
	return nil
}

// announceMuteOnAccept tells the device that just answered an outgoing call
// about a mute chosen while it rang.
func (e *engine) announceMuteOnAccept(c *Call) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181/src/voip/facade.rs#L3870-L3875
	if c == nil || !c.IsMuted() {
		return
	}
	go func() {
		if err := e.setMuted(c, true); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", c.id).Msg("announce mute chosen while ringing failed")
		}
	}()
}

// connectedGroupPeers lists the other connected participants of a group call
// we are connected in, as bare user JIDs; nil for a 1:1 call.
func (e *engine) connectedGroupPeers(callID string) []types.JID {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	if m == nil || !m.group || m.groupUpdate == nil || !selfConnected(*m.groupUpdate, m.selfLID) {
		return nil
	}
	self, _ := types.ParseJID(m.selfLID)
	var peers []types.JID
	for _, p := range m.groupUpdate.Participants {
		if p.State == "connected" && p.JID.User != self.User {
			peers = append(peers, p.JID.ToNonAD())
		}
	}
	return peers
}

// announceMuteToNewPeers sends our mute state to each participant that has
// connected since we last told them, as WA's engine does when it sets up a
// participant's audio stream (create_and_connect_audio_stream_for_participant
// -> wa_system_make_and_send_mute_v2_msg). The phones in a tester's call did
// this on every join; whoever joined after us without it never heard us.
func (e *engine) announceMuteToNewPeers(callID string) {
	// Source of truth: WA VoIP wasm create_and_connect_audio_stream_for_participant (wa_system_make_and_send_mute_v2_msg).
	peers := e.connectedGroupPeers(callID)
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil {
		e.mu.Unlock()
		return
	}
	connected := make(map[string]bool, len(peers))
	var fresh []types.JID
	for _, p := range peers {
		connected[p.User] = true
		if !m.muteAnnounced[p.User] {
			fresh = append(fresh, p)
		}
	}
	for user := range m.muteAnnounced {
		if !connected[user] {
			delete(m.muteAnnounced, user)
		}
	}
	if m.muteAnnounced == nil {
		m.muteAnnounced = map[string]bool{}
	}
	for _, p := range fresh {
		m.muteAnnounced[p.User] = true
	}
	call, creator := m.call, m.creator
	e.mu.Unlock()
	if len(fresh) == 0 {
		return
	}
	state := muteStateAttr(call.IsMuted())
	for _, peer := range fresh {
		node := signaling.BuildMuteV2(callID, peer, creator, state)
		node.Attrs["id"] = e.nextCallNodeID()
		if err := e.transmitCallNode(context.Background(), node); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", callID).Str("to", peer.String()).Msg("send mute state to new participant failed")
			e.mu.Lock()
			if m := e.calls[callID]; m != nil {
				delete(m.muteAnnounced, peer.User)
			}
			e.mu.Unlock()
			continue
		}
		e.c.log.Info().Str("call_id", callID).Str("to", peer.String()).Str("mute_state", state).Msg("told new participant our mute state")
		// Phones also send each participant they connect to a type-4
		// transport naming their network, which the peer's bandwidth
		// estimate reads (wa_transport_p2p_set_peer_net_medium).
		msgType := "4"
		transport := signaling.BuildTransport(&signaling.TransportParams{
			CallID: callID, To: peer, CallCreator: creator, TransportMessageType: &msgType,
		})
		transport.Attrs["id"] = e.nextCallNodeID()
		if err := e.transmitCallNode(context.Background(), transport); err != nil {
			e.c.log.Debug().Err(err).Str("call_id", callID).Str("to", peer.String()).Msg("send network medium to new participant failed")
		}
	}
}
