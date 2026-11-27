// Package p2p is the direct (peer-to-peer) path of a 1:1 call, beside the
// relay, in WA Web's "web P2P" form: ICE, DTLS, then the wa-web-p2p SCTP
// data channel.
package p2p

import (
	"context"
	"errors"
	"net/netip"

	"github.com/purpshell/meowcaller/signaling"
)

// DataChannelLabel and DataChannelID name WA Web's P2P data channel.
const (
	DataChannelLabel = "wa-web-p2p"
	DataChannelID    = 0
)

// FallbackSTUNServer is the STUN server WA Web adds after the call's relays.
const FallbackSTUNServer = "edge-stun.facebook.com:3478"

var errNotImplemented = errors.New("p2p: not implemented")

// VoipSettings are the server call settings that gate P2P.
type VoipSettings struct {
	DisableP2P   bool
	EnableWebP2P bool
}

// Config describes one call's P2P attempt.
type Config struct {
	CallID        string
	IsCaller      bool
	PeerIsWeb     bool
	STUNServers   []netip.AddrPort
	SendTransport func(*signaling.WebP2PTransport) error
}

// Conn is one direct path to the peer.
type Conn struct {
	cfg config
}

// Allowed reports whether a call may try the direct path.
func Allowed(isGroup, protectIP, callerIsContact, isCaller bool, settings VoipSettings) bool {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L42
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L80-L83
	switch {
	case isGroup, protectIP, settings.DisableP2P, !settings.EnableWebP2P:
		return false
	case !isCaller && !callerIsContact:
		return false
	}
	return true
}

// Dial starts ICE with the peer and returns once local credentials exist; the
// path opens later, or the attempt fails and the call stays on the relay.
func Dial(ctx context.Context, cfg Config, opts ...Option) (*Conn, error) {
	// Source of truth: WAWebVoipP2PConnectionManager.initP2PConnection, 2.3000.1056418802; see datasheets/web-p2p.md
	// TODO
	// agent suggestion: pion/ice agent (UDP host + srflx, STUN servers = up to 10 relay addresses + FallbackSTUNServer, controlling when caller), send our credentials + DTLS fingerprint at once and each candidate as gathered (priority>>24 capped 255, no .local, no TCP), 30 s connect timeout; DTLS client when caller and peer is web, server otherwise; then the SCTP channel DataChannelLabel/DataChannelID with the relay's channel options.
	// human input:
	return nil, errNotImplemented
}

// LocalTransport is our current credentials and candidates as a type 13 transport.
func (c *Conn) LocalTransport() *signaling.WebP2PTransport {
	// Source of truth: WAWebVoipP2PConnectionManager (Re/Le credentials, tt candidate send), 2.3000.1056418802; see datasheets/web-p2p.md
	// TODO
	// agent suggestion: return a copy of the stored ufrag/pwd, DTLS fingerprint (sha-256) and gathered candidates.
	// human input:
	return nil
}

// AddRemote applies the peer's type 13 transport.
func (c *Conn) AddRemote(t *signaling.WebP2PTransport) error {
	// Source of truth: WAWebVoipP2PConnectionManager.handleRemoteCredentials/handleRemoteCandidate, 2.3000.1056418802; see datasheets/web-p2p.md
	// TODO
	// agent suggestion: set remote ufrag/pwd + fingerprint once; a new ufrag means a peer ICE restart, so tear down and redial; buffer up to 20 candidates until credentials are applied, then add each as priority<<24 host UDP.
	// human input:
	return errNotImplemented
}

// Send writes one packet on the open data channel.
func (c *Conn) Send(pkt []byte) error {
	// Source of truth: WAWebVoipP2PConnectionManager.sendP2PData, 2.3000.1056418802; see datasheets/web-p2p.md
	// TODO
	// agent suggestion: write to the SCTP channel only while it is open; return an error otherwise so the caller keeps the relay.
	// human input:
	return errNotImplemented
}

// Recv returns the next packet from the data channel.
func (c *Conn) Recv(ctx context.Context) ([]byte, error) {
	// Source of truth: WAWebVoipStackInterfaceWeb registerOnDataChannelMessage -> handleOnMessageFromHeap, 2.3000.1056418802; see datasheets/web-p2p.md
	// TODO
	// agent suggestion: read one SCTP message and hand it to the engine's relay receive path unchanged (WA feeds both into handleOnMessageFromHeap).
	// human input:
	return nil, errNotImplemented
}

// Close ends the P2P attempt.
func (c *Conn) Close() error {
	// Source of truth: WAWebVoipP2PConnectionManager.cleanupP2PConnection, 2.3000.1056418802; see datasheets/web-p2p.md
	// TODO
	// agent suggestion: close the channel, DTLS and ICE agent; safe to call twice.
	// human input:
	return errNotImplemented
}
