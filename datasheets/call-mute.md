<!-- Datasheet = three things only: the reference source VERBATIM, the Go envelope
     (signatures, no bodies), and implementation suggestions. No behavioral summary,
     no implementation. The verbatim source is the only authoritative content. -->

# Datasheet: `meowcaller/engine` local mute announcement

Announcing this client's own microphone state (`<mute_v2>`) and muting the
outbound stream with it.

**Validation vector:** `engine_mute_test.go` — mute on an answered incoming call
sends `mute-state="1"` to the caller and silences outbound frames; unmute waits for
the stanza; an unanswered outgoing call records the state locally only and
announces it when the peer accepts; a group call addresses `<call-id>@call`; the
answer path carries the current state instead of a fixed `"0"`.

**Reference pinned at:** `3987f7c809a0b1ca3296a0e9a4fbb7ce96ea3181`

## Reference source (verbatim — authoritative excerpt)

`src/voip/facade.rs` L3860-L3936:

```rust
    /// Mute or unmute the local microphone and tell the other side.
    ///
    /// While muted the engine sends DTX comfort-noise (the stream stays fed) instead of gapping, so the
    /// peer doesn't re-negotiate the transport. This affects PCM audio only; encoded-audio callers must
    /// mute their external source.
    ///
    /// The state is announced as `<mute_v2>`, addressed like the rest of this call's signaling: the
    /// call scope for a group or call-link call, whose roster shows who is muted, and the device that
    /// answered for a direct call. Muting takes effect locally whatever becomes of the stanza, so an
    /// `Err` there means the peer still shows this side open while it is really muted. Unmuting waits
    /// for the announcement, so an `Err` there means the microphone is still muted. Dropping this
    /// future resolves the same way: the microphone is never left live while the peer shows it muted.
    ///
    /// An outgoing call nobody has answered yet is muted locally only: the state belongs to a call
    /// that does not exist on any of the devices still ringing, and answering does not replay it. Call
    /// this again once the call is up to announce a state chosen while it rang.
    pub async fn set_muted(&self, muted: bool) -> Result<(), CallError> {
        self.ensure_current()?;
        let client = self.upgrade_client()?;
        // One ordered transition per call: two cloned handles muting at once would otherwise race,
        // and the older value could land on the wire after the newer one.
        let _transition = client.lock_answer_transition(&self.call_id).await;
        // Re-checked under the lane: a call that ended while we waited must report that, not the
        // `Ok(())` a live call still ringing gets.
        self.ensure_current()?;
        let Some(target) = self.mute_target() else {
            // Nobody to mislead: a call still ringing has no answered call anywhere to carry the
            // state. The answer path does not replay it either, so a caller that mutes before the
            // callee picks up has to set it again once the call is up.
            self.muted.store(muted, Ordering::Relaxed);
            return Ok(());
        };
        // The two directions commit around the announcement rather than at one point, because either
        // half can be lost: the stanza to a failed send, the local half to a caller that drops this
        // future (a timeout, a `select!`) while the send is in flight. Muting applies first and
        // unmuting only once the announcement is out, so whatever is lost, the microphone is never
        // live while the peer is still showing this side muted.
        if muted {
            self.muted.store(true, Ordering::Relaxed);
        }
        client
            .voip()
            .announce_muted_locked(
                &self.call_id,
                &target,
                &self.call_creator,
                self.generation,
                muted,
            )
            .await
            .inspect(|()| self.muted.store(muted, Ordering::Relaxed))
    }

    /// Where this call's mute state can land: the call scope once it is a group call, the peer device
    /// otherwise. An outgoing call has one only after an `<accept>` names the device that answered;
    /// an incoming call we answered has had the caller's device since the offer.
    fn mute_target(&self) -> Option<Jid> {
        if self
            .client_registry
            .group_state_if_current(&self.call_id, self.generation)
            .is_some()
        {
            return Some(Jid::new(&self.call_id, Server::Call));
        }
        let session = self
            .client_registry
            .snapshot_if_current(&self.call_id, self.generation)?;
        match session.direction {
            CallDirection::Incoming => Some(session.peer_jid),
            _ => session.answering_device,
        }
    }

    /// Whether the microphone is currently muted.
    pub fn is_muted(&self) -> bool {
        self.muted.load(Ordering::Relaxed)
    }
```

`src/client/voip.rs` L1746-L1770:

```rust
    pub(crate) async fn announce_muted_locked(
        &self,
        call_id: &str,
        peer: &Jid,
        call_creator: &Jid,
        generation: u64,
        muted: bool,
    ) -> Result<(), CallError> {
        // A handle superseded while it waited must not announce a state for the replacement that now
        // owns this call-id.
        if self.client.call_registry().generation_of(call_id) != Some(generation) {
            return Err(CallError::Media("call is no longer active"));
        }
        self.send_group_control(
            call_id,
            build_mute_v2(
                call_id,
                peer,
                call_creator,
                &self.client.generate_request_id(),
                muted,
            ),
        )
        .await
    }
```

`wacore/src/stanza/call.rs` L1165-L1180:

```rust
/// `<call to=peer id=wrapper_id><mute_v2 call-id call-creator mute-state="1|0"/></call>`: this
/// side announcing its own microphone state. `mute-state` is the boolean sibling of
/// `raise-hand-state`, which the same signaling carries in the same shape.
pub fn build_mute_v2(
    call_id: &str,
    to: &Jid,
    call_creator: &Jid,
    wrapper_id: &str,
    muted: bool,
) -> Node {
    let action = NodeBuilder::new("mute_v2")
        .attr("call-id", call_id)
        .attr("call-creator", call_creator)
        .attr("mute-state", if muted { "1" } else { "0" })
        .build();
    call_wrap(to, Some(wrapper_id), action)
```

## Go envelope

```go
// SetMuted mutes or unmutes this client's microphone and announces it.
func (c *Call) SetMuted(muted bool) error

// IsMuted reports whether the microphone is muted.
func (c *Call) IsMuted() bool

func (e *engine) setMuted(c *Call, muted bool) error
func (e *engine) muteTarget(callID string) (to, creator types.JID, ok bool)
```

## Implementation suggestions

- Target: `m.from` already holds the reference's `mute_target` (`<call-id>@call`
  for a group, the answering device after `onAccept`, the caller for an incoming
  call). An outgoing 1:1 call before `markPeerAccepted` and an incoming call before
  `answer` have no target: store the state only.
- Mute applies locally before the stanza; unmute only after it is sent.
- Outbound audio: while muted the send loop encodes silence (the stream stays fed).
- Deviation (documented): the reference leaves a state chosen while ringing for the
  caller to re-announce; meowcaller announces it from `onAccept`, and `answer`
  sends the current state in its own `mute_v2` instead of a fixed `"0"`.
