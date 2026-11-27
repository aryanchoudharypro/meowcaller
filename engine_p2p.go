package meowcaller

import (
	"context"
	"errors"
	"net/netip"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/purpshell/meowcaller/p2p"
	"github.com/purpshell/meowcaller/signaling"
)

// SetProtectIP sets "Protect IP address in calls" (WhatsApp's
// setting_relayAllCalls): on, no call tries a direct path to the peer.
func (c *Client) SetProtectIP(on bool) {
	c.eng.protectIP.Store(on)
}

// SetCallerIsContact sets how an incoming caller is checked against the
// saved contacts; WhatsApp tries no direct path for a caller who is not one.
func (c *Client) SetCallerIsContact(fn func(types.JID) bool) {
	if fn == nil {
		c.eng.callerIsContact.Store(nil)
		return
	}
	c.eng.callerIsContact.Store(&fn)
}

// startWebP2P tries the direct path for a 1:1 call next to its relays, the
// way WA Web's initP2PConnectionIfEnabled does once the relay list is known.
func (e *engine) startWebP2P(ctx context.Context, callID string, ch *relayFanout, rd *relayData, inbound bool) {
	// NOT VALIDATED: no live call has opened a direct path (the server sends disable_p2p=1).
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L42
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.group || m.p2pConn != nil {
		e.mu.Unlock()
		return
	}
	settings := m.p2pSettings
	to, creator := m.from, m.creator
	call := m.call
	e.mu.Unlock()
	if to.IsEmpty() {
		return
	}

	contact := true
	if inbound {
		contact = false
		if fn := e.callerIsContact.Load(); fn != nil && !creator.IsEmpty() {
			contact = (*fn)(creator.ToNonAD())
		}
	}
	if !p2p.Allowed(false, e.protectIP.Load(), contact, !inbound, settings) {
		e.c.log.Debug().Str("call_id", callID).
			Bool("protect_ip", e.protectIP.Load()).
			Bool("disable_p2p", settings.DisableP2P).
			Bool("enable_web_p2p", settings.EnableWebP2P).
			Bool("caller_is_contact", contact).
			Msg("p2p: not trying a direct path")
		return
	}
	peerIsWeb := call != nil && call.PeerPlatform() == "web"
	cfg := p2p.Config{
		CallID: callID, IsCaller: !inbound, PeerIsWeb: peerIsWeb,
		STUNServers: relaySTUNServers(rd),
		SendTransport: func(t *signaling.WebP2PTransport) error {
			node := signaling.BuildWebP2PTransport(to, creator, t, "2", e.c.log)
			node.Attrs["id"] = e.nextCallNodeID()
			return e.transmitCallNode(context.Background(), node)
		},
	}
	conn, err := p2p.Dial(ctx, cfg, p2p.WithLogger(e.c.log))
	if err != nil {
		e.c.log.Debug().Err(err).Str("call_id", callID).Msg("p2p: dial failed, staying on relay")
		return
	}
	e.mu.Lock()
	if m = e.calls[callID]; m == nil || m.p2pConn != nil {
		e.mu.Unlock()
		_ = conn.Close()
		return
	}
	m.p2pConn = conn
	e.mu.Unlock()
	context.AfterFunc(ctx, func() { _ = conn.Close() })
	ch.attachP2P(conn)
	go func() {
		<-conn.Done()
		e.mu.Lock()
		if m := e.calls[callID]; m != nil && m.p2pConn == conn {
			m.p2pConn = nil
		}
		e.mu.Unlock()
		ch.detachP2P(conn)
	}()
}

// onWebP2PTransport takes a type 13 transport; it reports false for any
// other transport so the relay path handles it.
func (e *engine) onWebP2PTransport(ev *events.CallTransport) bool {
	if ev.Data == nil {
		return false
	}
	tr := ev.Data
	if tr.Tag != "transport" {
		if t := findChild(tr, "transport"); t != nil {
			tr = t
		}
	}
	if typ, _ := tr.Attrs["transport-message-type"].(string); typ != signaling.TransportTypeWebP2P {
		round, _ := tr.Attrs["p2p-cand-round"].(string)
		te := 0
		for _, c := range tr.GetChildren() {
			if c.Tag == "te" || c.Tag == "te2" {
				te++
			}
		}
		e.c.log.Debug().Str("call_id", ev.CallID).Str("transport_type", typ).Str("p2p_cand_round", round).
			Int("te_count", te).Msg("p2p: peer transport seen")
		return false
	}
	t, err := signaling.ParseWebP2PTransport(*tr, e.c.log)
	if err != nil {
		e.c.log.Warn().Err(err).Str("call_id", ev.CallID).Msg("p2p: bad type 13 transport")
		return true
	}
	e.mu.Lock()
	m := e.calls[ev.CallID]
	var conn *p2p.Conn
	if m != nil {
		conn = m.p2pConn
	}
	e.mu.Unlock()
	if conn == nil {
		e.c.log.Debug().Str("call_id", ev.CallID).Msg("p2p: type 13 transport with no direct path running")
		return true
	}
	if err := conn.AddRemote(t); errors.Is(err, p2p.ErrPeerRestart) {
		// WA Web rebuilds the whole connection on a peer ICE restart; the
		// call stays on its relays meanwhile and does not try again here.
		e.c.log.Info().Str("call_id", ev.CallID).Msg("p2p: peer restarted ice, closing the direct path")
		_ = conn.Close()
	} else if err != nil {
		e.c.log.Debug().Err(err).Str("call_id", ev.CallID).Msg("p2p: remote transport not applied")
	}
	return true
}

// relaySTUNServers lists the call's relay addresses, as WA Web passes them to
// its ICE agent as STUN servers.
func relaySTUNServers(rd *relayData) []netip.AddrPort {
	if rd == nil {
		return nil
	}
	var out []netip.AddrPort
	for _, ep := range rd.endpoints {
		for _, a := range ep.addresses {
			if ip, err := netip.ParseAddr(a.ipv4); err == nil && a.port != 0 {
				out = append(out, netip.AddrPortFrom(ip, a.port))
			}
		}
	}
	return out
}
