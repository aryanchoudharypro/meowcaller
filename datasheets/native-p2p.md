<!-- Datasheet = three things only: the reference source VERBATIM, the Go envelope
     (signatures, no bodies), and implementation suggestions. No behavioral summary,
     no implementation. The verbatim source is the only authoritative content. -->

# Datasheet: `meowcaller/p2p` native peer-to-peer call path

The direct UDP path a phone or desktop app opens to the other side of a 1:1
call, beside the relay. Candidates (`<te>` address entries) travel in
`<transport>` stanzas with `p2p-cand-round`; the two sides then check each
candidate pair with STUN binding requests sent straight to the peer, and media
moves to the first pair that answers. Like web P2P it is tried only when
"Protect IP address in calls" is off and the server allows P2P.

**Validation vector:** none. A decision point for the human (AGENTS.md
directive 4). The server sends `"disable_p2p":"1"` in every `voip_settings`
we have, and our logs never recorded a raw peer `<transport>`. The engine now
logs each peer transport's type, round and `<te>` count (debug level), so the
next call log shows whether phones send us candidates at all.

**Reference pinned at:** `UNMAPPED` — whatsapp-rust has no P2P path. The source
below is a decompiled reading of WA's VoIP wasm `-qEX3G_xaHP.wasm` (2026-11-11
bundle, `wa-bundles.zip`), functions named by their log strings
(`xplat/wa-voip/wacall/network/transport/wa_transport_p2p.cc`,
`wa_transport_util.cc`, `wa_stun_msg.cc`). It is not source text.

## Reference source (verbatim — authoritative excerpt)

### Two STUN modes (`wa_tp_p2p.cc` send_request, func 12025)

`send_request` skips private addresses when private P2P is disabled and
non-routable addresses, takes an "op key" (an outgoing packet slot; "failed to
acquire opkey, dropping pkt"), then builds the request one of two ways:

- func 12046 true (ICE credentials set): indirect 8706 = func 11773
  `build_rfc_stun_binding_msg` — RFC 8445 check with USERNAME `remote:local`
  (`%s:%s`), ICE-CONTROLLING or ICE-CONTROLLED, PRIORITY, USE-CANDIDATE, and
  MESSAGE-INTEGRITY keyed with the ICE password ("RFC STUN bind: missing ICE
  credentials").
- otherwise: indirect 8562 = func 11771 `build_stun_binding_msg`, called as
  `(tp, buf, 4096, tp->call+2873, 16, tid, type=1, priority=0)`.

### `build_stun_binding_msg` (func 11771), WAT

```
  (func (;11771;) (type 338) (param i32 i32 i32 i32 i32 i32 i32 i32) (result i64)
    (local i32 i64)
    global.get 0
    i32.const 32
    i32.sub
    local.tee 8
    global.set 0
    block ;; label = @1
      local.get 6
      i32.const 65279
      i32.and
      i32.const 1
      i32.ne
      if ;; label = @2
        i32.const 0
        i32.const 861777   ;; "xplat/wa-voip/wacall/network/transport/wa_transport_util.cc"
        i32.const 478942   ;; "build_stun_binding_msg"
        i32.const 92
        call 4695
        i64.const -1
        local.set 9
        br 1 (;@1;)
      end
      local.get 8
      i32.const 0
      i32.store offset=28
      local.get 8
      local.get 2
      i32.store offset=24
      local.get 8
      local.get 1
      i32.store offset=20
      local.get 8
      i32.const 0
      i32.store offset=16
      local.get 8
      local.get 2
      i32.store offset=12
      local.get 8
      local.get 1
      i32.store offset=8
      i64.const -1
      local.set 9
      local.get 8
      i32.const 20
      i32.add
      local.tee 1
      local.get 6
      local.get 5
      local.get 0
      i32.load offset=52
      i32.load offset=2844
      call 11772
      br_if 0 (;@1;)
      block ;; label = @2
        local.get 7
        i32.eqz
        br_if 0 (;@2;)
        local.get 1
        i32.const 36
        local.get 7
        call 11657
        i32.eqz
        br_if 0 (;@2;)
        i32.const 861837   ;; "wa_tp_util.cc"
        i32.const 0
        i32.const 607629   ;; "failed to add priority attribute for stun bind"
        i32.const 0
        call 4608
        br 1 (;@1;)
      end
      local.get 8
      i32.const 20
      i32.add
      local.get 8
      i32.const 8
      i32.add
      i32.const 1
      i32.const 0
      local.get 3
      local.get 4
      call 11662
      if ;; label = @2
        local.get 8
        local.get 6
        i32.store
        i32.const 861837   ;; "wa_tp_util.cc"
        i32.const 0
        i32.const 971344   ;; "failed to update stun msg length and integrity for message type %d."
        local.get 8
        call 4608
        br 1 (;@1;)
      end
      local.get 8
      i64.load32_u offset=28
      local.set 9
    end
    local.get 8
    i32.const 32
    i32.add
    global.set 0
    local.get 9
  )
```

Reading: message type must be binding (`type & 0xFEFF == 1`, request 0x0001 or
response 0x0101). `build_stun_req_hdr` (func 11772 → 11656) writes the 20-byte
header with the magic chosen by `call+2844`. PRIORITY (0x0024) is added only
when non-zero. Func 11662 then appends MESSAGE-INTEGRITY (HMAC-SHA1) keyed with
the 16 bytes at `call+2873`.

### Peer message test (`is_peer_msg`, func 12021), WAT

```
  (func (;12021;) (type 5) (param i32 i32 i32) (result i32)
    (local i32)
    block ;; label = @1
      local.get 2
      i32.const 44
      i32.lt_s
      br_if 0 (;@1;)
      local.get 1
      i32.load8_u
      i32.const 63
      i32.gt_u
      br_if 0 (;@1;)
      block ;; label = @2
        local.get 1
        i32.load offset=4 align=1
        call 14528
        local.tee 1
        i32.const 1952540791
        i32.ne
        br_if 0 (;@2;)
        local.get 0
        i32.load offset=52
        local.tee 0
        i32.load offset=2844
        br_if 0 (;@2;)
        local.get 0
        i32.const 1
        i32.store offset=2844
        i32.const 858070   ;; "wa_tp_p2p.cc"
        i32.const 0
        i32.const 478355   ;; "stun_sig: Changing over to stun signature magic in is_peer_msg"
        i32.const 0
        call 4615
        i32.const 1
        return
      end
      local.get 1
      i32.const 554869826
      i32.eq
      local.get 1
      i32.const 1952540791
      i32.eq
      i32.or
      local.set 3
    end
    local.get 3
  )
```

Reading: a peer STUN message is at least 44 bytes, first byte < 64, and its
cookie (bytes 4..8, big-endian) is `0x2112A442` or `0x74616877`. The first
message seen with `0x74616877` sets `call+2844` ("Changing over to stun
signature magic"), and from then on our own requests use that cookie.
`parse_incoming_peer_stun_msg` (func 12022) drops a message whose cookie or
MESSAGE-INTEGRITY fails, logs "Received a valid e2e bind probe response from
P2P" on a good response, and rejects P2P messages on the callee side before
the call is accepted.

### P2P key (`generate_srtp_and_p2p_keys_for_participant` 12850 → `derive_e2e_keys` 12855 → func 740)

`derive_e2e_keys` calls a function pointer (memory 1583504, set to table index
317 = func 740 at init) that fills a struct, then copies 30 bytes from offset 0
(SRTP) and 16 bytes from offset 260 (P2P key). Func 740, WAT:

```
  memory.fill out46, 0, 46
  call 1301 native_hkdf_extract_and_expand(out46, 46, callKey, callKeyLen, salt=0, saltLen=0, info=participant, strlen(participant))
  memcpy(struct+0,   out46+0,  30); struct.srtpLen(+256) = 30
  memcpy(struct+260, out46+30, 16); struct.p2pLen(+292)  = 16
```

So the P2P key is bytes [30:46] of the same 46-byte HKDF output meowcaller
already uses for SRTP ([0:30]). Read from the code, not assumed.

## Go envelope

```go
// NativeMagic is the "stun signature" cookie a native peer may switch to.
const NativeMagic uint32 = 0x74616877

// NativeKey is the 16-byte MESSAGE-INTEGRITY key for native peer binds.
func NativeKey(e2eKeys []byte) ([16]byte, error)

// IsNativePeerMsg reports whether b is a peer STUN message and which cookie it uses.
func IsNativePeerMsg(b []byte) (magic uint32, ok bool)

// BuildNativeBind builds a legacy peer binding request or response.
func BuildNativeBind(msgType uint16, tid [12]byte, magic uint32, priority uint32, key [16]byte) []byte

// VerifyNativeBind checks a peer bind's cookie and MESSAGE-INTEGRITY.
func VerifyNativeBind(b []byte, key [16]byte) error

// NativeConn checks peer candidates over UDP and carries media on the first pair that answers.
type NativeConn struct{ /* unexported */ }
func DialNative(ctx context.Context, cfg NativeConfig, opts ...Option) (*NativeConn, error)
func (c *NativeConn) AddRemote(cands []netip.AddrPort)
func (c *NativeConn) Send(pkt []byte) error
func (c *NativeConn) Recv() <-chan []byte
func (c *NativeConn) Close() error
```

## Implementation suggestions (guidance, not proof)

- ASSUMPTION: after a pair answers, media is the same raw SRTP/RTCP we send
  to a native (UDP) relay, sent to the peer address.
- Our candidates: host addresses of the media socket plus the XOR-MAPPED
  address from relay binds, as 6-byte `<te priority>` entries in our
  `<transport>` (type 1 already carries `p2p-cand-round="1"`).
- Reuse `stun.EncodeStunRequest` for the RFC-cookie case; the WA cookie needs
  only the header cookie changed before integrity is computed.
