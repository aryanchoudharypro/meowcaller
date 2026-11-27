package signaling

import (
	"errors"
	"net/netip"

	"github.com/rs/zerolog"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// TransportTypeWebP2P is the transport-message-type of a web P2P transport.
const TransportTypeWebP2P = "13"

var errWebP2PNotImplemented = errors.New("signaling: web p2p transport not implemented")

// WebP2PCandidate is one address in a web P2P transport.
type WebP2PCandidate struct {
	IP       netip.Addr
	Port     uint16
	Priority uint8
}

// WebP2PTransport is the content of <transport transport-message-type="13">.
type WebP2PTransport struct {
	CallID          string
	Round           string
	ICEUfrag        string
	ICEPwd          string
	CertAlgorithm   string
	CertFingerprint string
	Candidates      []WebP2PCandidate
}

// BuildWebP2PTransport builds the type 13 transport carrying our ICE
// credentials, DTLS fingerprint and candidates.
func BuildWebP2PTransport(to, creator types.JID, t *WebP2PTransport, medium string, log ...zerolog.Logger) waBinary.Node {
	// Source of truth: WA VoIP wasm serialize_transport (func 14132, stanzas/transport.cc) via WAWebVoipStackInterfaceWeb.sendWebP2PTransport, 2.3000.1056418802; see datasheets/web-p2p.md
	// TODO
	// agent suggestion: transport attrs call-id, call-creator, transport-message-type=13, p2p-cand-round when set, ice-ufrag/ice-pwd when set; children in wasm order: one <te priority=N> per candidate (4- or 16-byte IP + big-endian port), <net medium=M protocol=0>, <certificate algorithm fingerprint> only when both are set; wrap with callWrap(to, nil, ...).
	// human input:
	return waBinary.Node{}
}

// ParseWebP2PTransport reads a received type 13 transport.
func ParseWebP2PTransport(node waBinary.Node, log ...zerolog.Logger) (*WebP2PTransport, error) {
	// Source of truth: WA VoIP wasm deserialize_transport (func 14133) + WAWebVoipJsonParsersWeb.parseP2PTransportUpdateData, 2.3000.1056418802; see datasheets/web-p2p.md
	// TODO
	// agent suggestion: require transport-message-type=13; read ice-ufrag/ice-pwd attrs and <certificate algorithm fingerprint> (each optional, like the wasm's "attribute missing" warnings, since candidate-only updates carry none); decode each <te> as 6 or 18 bytes, skip any other length with a warn log.
	// human input:
	return nil, errWebP2PNotImplemented
}
