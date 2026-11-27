<!-- Datasheet = three things only: the reference source VERBATIM, the Go envelope
     (signatures, no bodies), and implementation suggestions. No behavioral summary,
     no implementation. The verbatim source is the only authoritative content. -->

# Datasheet: `meowcaller/p2p` web peer-to-peer call path

A direct path to the peer of a 1:1 call, beside the relay, the way WA Web's
"web P2P" works: ICE with the peer, then DTLS and an SCTP data channel named
`wa-web-p2p`, carrying the same packets the relay data channel carries. It is
signalled with `<transport transport-message-type="13">`. It is tried only when
"Protect IP address in calls" (`setting_relayAllCalls`, WA's `disallowAllP2p` /
`setHideMyIp`) is off and the server's call settings allow web P2P.

**Validation vector:** none yet. A decision point for the human (AGENTS.md
directive 4). The only possible vector is a captured type 13 exchange between a
phone and WA Web, and in every `voip_settings` we have (aryan's 2026-11-03 logs)
the server sends `"disable_p2p":"1"` and no web P2P enable key, so no such
exchange has been seen.

**Reference pinned at:** `UNMAPPED` — whatsapp-rust (checked at
`287e5344b90f30025dcecfd83325459cf88c7b8c`) has no web P2P path: only the
`p2p-cand-round` attribute of `<transport>` (`wacore/src/stanza/call.rs`). The
source below is WA Web itself: JS from the 2026-11-27 bundle
(2.3000.1056418802), and the wasm serializer read from the VoIP wasm
`-qEX3G_xaHP.wasm` (2026-11-11 bundle, `wa-bundles.zip`). The wasm part is a
decompiled reading, not source text.

## Reference source (verbatim — authoritative excerpt)

### WA Web JS (`WAWebVoipP2PConnectionManager`, `WAWebVoipHandleNativeCallEvent`)

Constants, remote candidate to SDP, local SDP candidate to transport entry,
the start gate (`initP2PConnectionIfEnabled`), and applying a received type 13:

```js
oe={urls:"stun:edge-stun.facebook.com:3478"},ae=null,ie=null,le=null,se=!1,ue=0,ce={DATA_CHANNEL_LABEL:"wa-web-p2p",DATA_CHANNEL_ID:0,CONNECTION_TIMEOUT_MS:3e4,MAX_BUFFERED_CANDIDATES:20}

function Ue(e){var t=e.match(/candidate:\S+\s+\d+\s+\S+\s+(\d+)\s+(\S+)\s+(\d+)\s+typ\s+\S+/);if(t==null)return null;var n=parseInt(t[1],10),r=t[2],o=parseInt(t[3],10);if(r.endsWith(".local"))return null;var a=Math.min(255,Math.floor(n/Math.pow(2,24)));return{ip:r,port:o,priority:a}}

function ze(e){var t=e.priority*Math.pow(2,24);return"candidate:1 1 UDP "+t+" "+e.ip+" "+e.port+" typ host"}

function Ke(){return Ke=n("asyncToGeneratorRuntime").asyncToGenerator(function*(){var e=r("nullthrows")(yield o("WAWebVoipStackInterface").getVoipStackInterface());if(e.type==="web"){o("WAWebBackendApi").frontendFireAndForget("trackVoipActivity",{activity:"get_call_info",details:"p2p_init"});var t=yield e.getCallInfo();if(t===""){o("WALogger").LOG(z||(z=babelHelpers.taggedTemplateLiteralLoose(["voip: initP2PConnectionIfEnabled: Failed to get call info"]))),oe.initStarted=!1;return}var n=e.parsers.parseCallInfo(t);if(n.isGroupCall){oe.initStarted=!1;return}var a=n.callId,i=n.isCaller;if(yield o("WAWebVoipP2PConnectionManager").refreshP2PEnablement(a),!o("WAWebVoipP2PConnectionManager").isP2PEnabled()){o("WALogger").LOG(j||(j=babelHelpers.taggedTemplateLiteralLoose(["voip: initP2PConnectionIfEnabled: P2P disabled for callId=",""])),a);return}var l=n.participants.find(function(e){return e.isSelf!==!0}),s=(l==null?void 0:l.devicePlatform)===o("WAWebVoipWaCallEnums").ClientPlatform.Web;if(!i&&n.peerJid!=null&&(yield o("WAWebVoipContactUtils").isCallerNotContact(n.peerJid))){o("WALogger").LOG(K||(K=babelHelpers.taggedTemplateLiteralLoose(["voip: initP2PConnectionIfEnabled: non-contact, P2P gated ",""])),a);return}if(!oe.callIsActive){oe.initStarted=!1;return}o("WALogger").LOG(Q||(Q=babelHelpers.taggedTemplateLiteralLoose(["voip: initP2PConnectionIfEnabled: callId=",", isCaller=",", isPeerWebBrowser=",""])),a,String(i),String(s));var u=function(n,i,l,s,u){e.sendWebP2PTransport(a,n,i,l,s,u).catch(function(e){o("WALogger").ERROR(X||(X=babelHelpers.taggedTemplateLiteralLoose(["voip: sendWebP2PTransport failed"]))).catching(r("getErrorSafe")(e))})},c=10,d=new Set;if(oe.cachedRelayListData==null){o("WALogger").ERROR(Y||(Y=babelHelpers.taggedTemplateLiteralLoose(["voip: initP2PConnectionIfEnabled: cachedRelayListData null"])));return}var m=oe.cachedRelayListData;for(var p of m.relays){if(d.size>=c)break;for(var _ of p.addresses){if(d.size>=c)break;_.ipv4!=null&&_.port!=null&&d.add("stun:"+_.ipv4+":"+_.port),d.size<c&&_.ipv6!=null&&_.port_v6!=null&&d.add("stun:["+_.ipv6+"]:"+_.port_v6)}}var f=Array.from(d,function(e){return{urls:e}});o("WAWebVoipP2PConnectionManager").registerOnPeerIceRestart(He),yield o("WAWebVoipP2PConnectionManager").initP2PConnection(i,s,f,u)}}),Ke.apply(this,arguments)}function de(){return de=n("asyncToGeneratorRuntime").asyncToGenerator(function*(e){var t=r("nullthrows")(yield o("WAWebVoipStackInterface").getVoipStackInterface());if(t.type==="web"){var n=t.parsers.parseP2PTransportUpdateData(e);o("WAWebVoipP2PConnectionManager").handleRemoteCredentials({ufrag:n.ice_ufrag,pwd:n.ice_pwd,algorithm:n.cert_algorithm,fingerprint:n.cert_fingerprint});for(var a of n.candidates)o("WAWebVoipP2PConnectionManager").handleRemoteCandidate(a)}}),de.apply(this,arguments)}
```

`WAWebVoipJsonParsersWeb.parseP2PTransportUpdateData` (what the wasm hands JS for
a received type 13):

```js
return{ice_ufrag:n.ice_ufrag,ice_pwd:n.ice_pwd,cert_algorithm:n.cert_algorithm,cert_fingerprint:n.cert_fingerprint,candidates:((e=n.candidates)!=null?e:[]).map(function(e){return{ip:e.ip,port:e.port,priority:e.priority}})}
```

Data channel bytes go into the wasm through the same entry as relay bytes,
from a virtual address (`WAWebVoipStackInterfaceWeb`):

```js
var R=a.getWebP2PVirtualIpv4(),L=a.getWebP2PVirtualIpv6(),E=a.getWebP2PVirtualPort();o("WAWebVoipP2PConnectionManager").initP2PVirtualAddresses(R,L,E),o("WAWebVoipP2PConnectionManager").registerOnDataChannelMessage(function(e){var t=new Uint8Array(e),n=o("WAWebVoipWasmHeapBuffer").ensureHeapBuffer(a,i,t.byteLength);n!==0&&(a.GROWABLE_HEAP_U8().set(t,n),a.handleOnMessageFromHeap(n,t.byteLength,R,E))});
```

DTLS roles (`je`): peer is web: caller = DTLS client (remote `a=setup:passive`),
callee = DTLS server; peer is native: we are always DTLS server (remote
`a=setup:active`). ICE: caller controlling, callee controlled. TCP candidates
are never sent; `.local` (mDNS) candidates are dropped. Connection timeout 30 s,
then the call stays on the relay. A peer ICE restart (new ufrag) rebuilds the
whole P2P connection.

### WA VoIP wasm `serialize_transport` (func 14132, `stanzas/transport.cc`)

```
<transport call-id call-creator transport-message-type=T [p2p-cand-round=R]
           [ice-ufrag=U]   (T==13, if set)
           [ice-pwd=P]>    (T==13, if set)
  <te priority=N>ADDR</te>*      one per candidate; tag te2 when T==2
  <net medium=M [protocol=P]/>   [health_status=H when T==11]
  [<rte>ADDR</rte>]
  [<certificate algorithm=A fingerprint=F/>]   (T==13, both set)
</transport>
ADDR = IPv4 4 bytes + port 2 bytes (network order), or IPv6 16 bytes + port 2.
```

The wasm gate: `is_p2p_disabled` (func 14242): "disabled because
disallow_all_p2p is set" / "because caller is not in the contact list";
`setHideMyIp` sets `disallow_all_p2p`; `apply_callee_voip_params` reads
`enable_web_p2p`.

## Go envelope

```go
// package signaling

// WebP2PCandidate is one address in a type 13 transport.
type WebP2PCandidate struct {
	IP       netip.Addr
	Port     uint16
	Priority uint8
}

// WebP2PTransport is the content of <transport transport-message-type="13">.
type WebP2PTransport struct {
	CallID, Round                  string
	ICEUfrag, ICEPwd               string
	CertAlgorithm, CertFingerprint string
	Candidates                     []WebP2PCandidate
}

func BuildWebP2PTransport(to, creator types.JID, t *WebP2PTransport, medium string, log ...zerolog.Logger) waBinary.Node
func ParseWebP2PTransport(node waBinary.Node, log ...zerolog.Logger) (*WebP2PTransport, error)

// package p2p

// Allowed reports whether a call may try the direct path.
func Allowed(isGroup, protectIP, callerIsContact, isCaller bool, settings VoipSettings) bool

// Conn is one direct path to the peer: ICE, DTLS, then the wa-web-p2p channel.
type Conn struct{ /* unexported */ }

func Dial(ctx context.Context, cfg Config, opts ...Option) (*Conn, error)
func (c *Conn) LocalTransport() *signaling.WebP2PTransport
func (c *Conn) AddRemote(t *signaling.WebP2PTransport) error
func (c *Conn) Send(pkt []byte) error
func (c *Conn) Recv(ctx context.Context) ([]byte, error)
func (c *Conn) Close() error
```

## Implementation suggestions

- ICE: `pion/ice` (pion dtls/sctp are already dependencies). STUN servers as WA
  Web: the call's relay addresses (up to 10) plus `stun:edge-stun.facebook.com:3478`.
  UDP host and server-reflexive candidates only. Priority on the wire =
  ICE priority >> 24, capped at 255; rebuilt as `priority << 24` on receive.
- DTLS: reuse the relay's pion dtls setup with the roles above; check the peer
  certificate against the type 13 fingerprint. SCTP channel: label `wa-web-p2p`,
  id 0, the relay's channel options.
- Engine: after the relay list, if `Allowed`, send our type 13 (credentials
  first, then each candidate as it is found, as WA Web does); keep media on the
  relay until the channel opens, then send media over it; drop back to the relay
  on close/timeout. What the wasm sends over the channel after it opens (raw
  SRTP, or with its own STUN probes) is NOT known from the reading above; it is
  the open question a live capture would answer.
- Gate inputs: `protectIP` from whatSnap's `protect_ip_in_calls`; server
  settings `disable_p2p` / `enable_web_p2p`; callee side drops non-contacts.
