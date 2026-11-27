package signaling

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strconv"

	"github.com/rs/zerolog"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// TransportTypeWebP2P is the transport-message-type of a web P2P transport.
const TransportTypeWebP2P = "13"

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
	// NOT VALIDATED: layout read from the WA wasm serializer; validated once a captured phone/WA Web type 13 exchange exists (none while the server sends disable_p2p=1).
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L66-L78
	lg := pickLog(log)
	attrs := waBinary.Attrs{"call-id": t.CallID, "call-creator": creator, "transport-message-type": TransportTypeWebP2P}
	if t.Round != "" {
		attrs["p2p-cand-round"] = t.Round
	}
	if t.ICEUfrag != "" {
		attrs["ice-ufrag"] = t.ICEUfrag
	}
	if t.ICEPwd != "" {
		attrs["ice-pwd"] = t.ICEPwd
	}
	children := make([]waBinary.Node, 0, len(t.Candidates)+2)
	for _, c := range t.Candidates {
		addr := encodeWebP2PAddr(c.IP, c.Port)
		if addr == nil {
			lg.Warn().Msg("web p2p: skipping invalid candidate address")
			continue
		}
		children = append(children, waBinary.Node{Tag: "te", Attrs: waBinary.Attrs{"priority": strconv.Itoa(int(c.Priority))}, Content: addr})
	}
	children = append(children, waBinary.Node{Tag: "net", Attrs: waBinary.Attrs{"medium": medium, "protocol": "0"}})
	if t.CertAlgorithm != "" && t.CertFingerprint != "" {
		children = append(children, waBinary.Node{Tag: "certificate", Attrs: waBinary.Attrs{"algorithm": t.CertAlgorithm, "fingerprint": t.CertFingerprint}})
	}
	lg.Debug().
		Str("call_id", t.CallID).
		Bool("has_ufrag", t.ICEUfrag != "").
		Bool("has_fingerprint", t.CertFingerprint != "").
		Int("candidates", len(t.Candidates)).
		Msg("building web p2p transport")
	return callWrap(to, nil, waBinary.Node{Tag: "transport", Attrs: attrs, Content: children})
}

// ParseWebP2PTransport reads a received type 13 transport (the <transport>
// node, or the <call> around it).
func ParseWebP2PTransport(node waBinary.Node, log ...zerolog.Logger) (*WebP2PTransport, error) {
	// NOT VALIDATED: same capture as BuildWebP2PTransport.
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L45-L50
	lg := pickLog(log)
	if node.Tag == "call" {
		children := node.GetChildren()
		if len(children) == 0 {
			return nil, errors.New("signaling: empty call stanza")
		}
		node = children[0]
	}
	if node.Tag != "transport" {
		return nil, fmt.Errorf("signaling: want transport, got %q", node.Tag)
	}
	attrs := node.AttrGetter()
	if typ := attrs.OptionalString("transport-message-type"); typ != TransportTypeWebP2P {
		return nil, fmt.Errorf("signaling: transport-message-type %q is not web p2p", typ)
	}
	t := &WebP2PTransport{
		CallID:   attrs.OptionalString("call-id"),
		Round:    attrs.OptionalString("p2p-cand-round"),
		ICEUfrag: attrs.OptionalString("ice-ufrag"),
		ICEPwd:   attrs.OptionalString("ice-pwd"),
	}
	for _, child := range node.GetChildren() {
		switch child.Tag {
		case "te":
			ip, port, ok := decodeWebP2PAddr(nodeBytes(&child))
			if !ok {
				lg.Warn().Int("len", len(nodeBytes(&child))).Msg("web p2p: skipping candidate of unexpected length")
				continue
			}
			prio, err := strconv.Atoi(child.AttrGetter().OptionalString("priority"))
			if err != nil || prio < 0 || prio > 255 {
				prio = 0
			}
			t.Candidates = append(t.Candidates, WebP2PCandidate{IP: ip, Port: port, Priority: uint8(prio)})
		case "certificate":
			ca := child.AttrGetter()
			t.CertAlgorithm = ca.OptionalString("algorithm")
			t.CertFingerprint = ca.OptionalString("fingerprint")
		}
	}
	lg.Debug().
		Str("call_id", t.CallID).
		Bool("has_ufrag", t.ICEUfrag != "").
		Bool("has_fingerprint", t.CertFingerprint != "").
		Int("candidates", len(t.Candidates)).
		Msg("parsed web p2p transport")
	return t, nil
}

func encodeWebP2PAddr(ip netip.Addr, port uint16) []byte {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L77
	if !ip.IsValid() {
		return nil
	}
	ip = ip.Unmap()
	b := ip.AsSlice()
	return binary.BigEndian.AppendUint16(b, port)
}

func decodeWebP2PAddr(b []byte) (netip.Addr, uint16, bool) {
	// Source of truth: https://github.com/aryanchoudharypro/meowcaller/blob/b5c612d74da96f9ae704b9df4b3b0b9a6acfa028/datasheets/web-p2p.md#L77
	if len(b) != 6 && len(b) != 18 {
		return netip.Addr{}, 0, false
	}
	ip, ok := netip.AddrFromSlice(b[:len(b)-2])
	return ip, binary.BigEndian.Uint16(b[len(b)-2:]), ok
}
