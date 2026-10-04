package meowcaller

import (
	"slices"

	"go.mau.fi/whatsmeow/types"
)

// groupUpdateApply is what a group_update did to a call's committed snapshot.
type groupUpdateApply int

const (
	// groupUpdateStale: nothing changed.
	groupUpdateStale groupUpdateApply = iota
	// groupUpdateRelayOnly: an older roster supplied the first or a newer relay
	// allocation. The roster, and everything a roster drives (membership, rekey
	// requests, waiting-room admission), is untouched.
	groupUpdateRelayOnly
	// groupUpdateApplied: the roster advanced.
	groupUpdateApplied
)

// mergeGroupUpdate folds update into the call's committed snapshot. The roster
// and the relay allocation advance on independent transactions:
//
//   - the relay's transaction-id numbers relay allocations, not rosters, and the
//     snapshot that carries a relay can arrive after a newer roster-only one
//     (live: tx=13 with relay after tx=15 without), so an older roster may still
//     supply the first or a newer allocation;
//   - a roster-only update means "no allocation refresh", so the held relay is
//     carried across it;
//   - roster updates leave out the pid of devices whose pid did not change, so
//     an absent pid is inherited. Reading it as "no pid" drops the device from
//     the receive roster and from the relay subscription, and its audio stops.
func mergeGroupUpdate(current *groupCallUpdate, update groupCallUpdate) (groupCallUpdate, groupUpdateApply) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip_control/group.rs#L70-L190
	if current == nil {
		return cloneGroupCallUpdate(update), groupUpdateApplied
	}
	advancesRelay := update.Relay != nil &&
		(current.Relay == nil || update.Relay.TransactionID > current.Relay.TransactionID)
	if update.TransactionID <= current.TransactionID {
		if !advancesRelay {
			return groupCallUpdate{}, groupUpdateStale
		}
		merged := cloneGroupCallUpdate(*current)
		merged.Relay = cloneGroupCallUpdate(update).Relay
		return merged, groupUpdateRelayOnly
	}

	merged := cloneGroupCallUpdate(update)
	if merged.GroupJID.IsEmpty() {
		merged.GroupJID = current.GroupJID
	}
	known := make(map[types.JID]uint32)
	for _, participant := range current.Participants {
		for _, device := range participant.Devices {
			if device.HasPID {
				known[device.JID] = device.PID
			}
		}
	}
	// Exact namespace advertisements take precedence; the roster's own PN/LID
	// mapping is a fallback only.
	for _, participant := range current.Participants {
		for _, device := range participant.Devices {
			if !device.HasPID {
				continue
			}
			if alias, ok := mappedGroupDevice(participant.JID, participant.PN, device.JID); ok {
				if _, exists := known[alias]; !exists {
					known[alias] = device.PID
				}
			}
		}
	}
	for i := range merged.Participants {
		participant := &merged.Participants[i]
		if participant.State != "connected" {
			continue
		}
		for j := range participant.Devices {
			device := &participant.Devices[j]
			if device.HasPID {
				continue
			}
			pid, ok := known[device.JID]
			if !ok {
				var alias types.JID
				if alias, ok = mappedGroupDevice(participant.JID, participant.PN, device.JID); ok {
					pid, ok = known[alias]
				}
			}
			if ok {
				device.PID, device.HasPID = pid, true
			}
		}
	}
	if !advancesRelay {
		// Roster progress cannot revoke or roll back a held allocation.
		merged.Relay = cloneGroupCallUpdate(*current).Relay
	}
	return merged, groupUpdateApplied
}

// mappedGroupDevice returns device under its owner's other identity (LID for a
// PN device and the reverse), keeping the device suffix. PID continuity alone
// may use the roster's explicit PN/LID mapping; the stored JID is never
// rewritten, because media and Signal key derivations differ per namespace.
func mappedGroupDevice(owner, pn, device types.JID) (types.JID, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/e0225b3a/wacore/src/voip_control/group.rs#L247-L268
	if pn.IsEmpty() || owner.Server != types.HiddenUserServer || pn.Server != types.DefaultUserServer {
		return types.EmptyJID, false
	}
	var alternate types.JID
	switch device.ToNonAD() {
	case owner.ToNonAD():
		alternate = pn
	case pn.ToNonAD():
		alternate = owner
	default:
		return types.EmptyJID, false
	}
	alias := device
	alias.User = alternate.User
	alias.Server = alternate.Server
	return alias, true
}

// sortedUniquePIDs returns pids in the canonical order the relay subscription
// is compared in.
func sortedUniquePIDs(pids []uint32) []uint32 {
	out := slices.Clone(pids)
	slices.Sort(out)
	return slices.Compact(out)
}
