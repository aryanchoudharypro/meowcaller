package p2p

import (
	"context"
	"errors"
	"net/netip"
)

// NativeMagic is the "stun signature" cookie a native peer may switch to.
const NativeMagic uint32 = 0x74616877

var errNotImplemented = errors.New("p2p: not implemented")

// NativeConfig is what DialNative needs for one call.
type NativeConfig struct {
	CallID   string
	IsCaller bool
	Key      [16]byte
	Remote   []netip.AddrPort
}

// NativeConn checks peer candidates over UDP and carries media on the first
// pair that answers.
type NativeConn struct{}

// NativeKey is the 16-byte MESSAGE-INTEGRITY key for native peer binds.
func NativeKey(e2eKeys []byte) ([16]byte, error) {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/7c7b983ce80af1c09b620fd45fe902c0221c09d2/datasheets/native-p2p.md#L217-L221
	// TODO
	// agent suggestion: require len(e2eKeys) >= 46 and return e2eKeys[30:46] (wasm func 740: HKDF out[30:46]).
	// human input:
	return [16]byte{}, errNotImplemented
}

// IsNativePeerMsg reports whether b is a peer STUN message and which cookie it uses.
func IsNativePeerMsg(b []byte) (uint32, bool) {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/7c7b983ce80af1c09b620fd45fe902c0221c09d2/datasheets/native-p2p.md#L156-L215
	// TODO
	// agent suggestion: len >= 44, b[0] < 64, cookie = big-endian b[4:8] equal to 0x2112A442 or NativeMagic.
	// human input:
	return 0, false
}

// BuildNativeBind builds a legacy peer binding request or response.
func BuildNativeBind(msgType uint16, tid [12]byte, magic uint32, priority uint32, key [16]byte) ([]byte, error) {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/7c7b983ce80af1c09b620fd45fe902c0221c09d2/datasheets/native-p2p.md#L42-L154
	// TODO
	// agent suggestion: reject msgType&0xFEFF != 1; 20-byte header with the given cookie, PRIORITY 0x0024 only when non-zero, then MESSAGE-INTEGRITY (HMAC-SHA1 with key), no FINGERPRINT; reuse stun.EncodeStunRequest when magic is the RFC cookie.
	// human input:
	return nil, errNotImplemented
}

// VerifyNativeBind checks a peer bind's cookie and MESSAGE-INTEGRITY.
func VerifyNativeBind(b []byte, key [16]byte) error {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/7c7b983ce80af1c09b620fd45fe902c0221c09d2/datasheets/native-p2p.md#L206-L215
	// TODO
	// agent suggestion: IsNativePeerMsg, then recompute HMAC-SHA1 over the message up to the integrity attribute with the length field adjusted, constant-time compare.
	// human input:
	return errNotImplemented
}

// DialNative starts candidate checks toward the peer.
func DialNative(ctx context.Context, cfg NativeConfig, opts ...Option) (*NativeConn, error) {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/7c7b983ce80af1c09b620fd45fe902c0221c09d2/datasheets/native-p2p.md#L28-L40
	// TODO
	// agent suggestion: one UDP socket; every 50 ms send a bind request to each unanswered remote candidate (skip private/non-routable like send_request), answer peer requests with 0x0101, adopt NativeMagic once seen, elect the first pair that answers; 30 s timeout like web P2P.
	// human input:
	return nil, errNotImplemented
}

// AddRemote adds peer candidates from a later transport round.
func (c *NativeConn) AddRemote(cands []netip.AddrPort) {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/7c7b983ce80af1c09b620fd45fe902c0221c09d2/datasheets/native-p2p.md#L28-L40
	// TODO
	// agent suggestion: append unseen candidates (cap 20 like web P2P) and start checking them.
	// human input:
}

// Send writes one media packet to the elected peer address.
func (c *NativeConn) Send(pkt []byte) error {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/7c7b983ce80af1c09b620fd45fe902c0221c09d2/datasheets/native-p2p.md#L250-L260
	// TODO
	// agent suggestion: drop with an error until a pair is elected; then write the same raw SRTP/RTCP a native relay gets (ASSUMPTION).
	// human input:
	return errNotImplemented
}

// Recv delivers media packets from the elected peer address.
func (c *NativeConn) Recv() <-chan []byte {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/7c7b983ce80af1c09b620fd45fe902c0221c09d2/datasheets/native-p2p.md#L250-L260
	// TODO
	// agent suggestion: buffered channel fed by the read loop with every non-STUN packet from the elected address.
	// human input:
	return nil
}

// Close stops the checks and closes the socket.
func (c *NativeConn) Close() error {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/7c7b983ce80af1c09b620fd45fe902c0221c09d2/datasheets/native-p2p.md#L28-L40
	// TODO
	// agent suggestion: cancel the check loop, close the socket, close the Recv channel once.
	// human input:
	return nil
}
