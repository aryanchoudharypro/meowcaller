package meowcaller

import (
	"context"
	"fmt"

	"github.com/purpshell/meowcaller/signaling"
	"github.com/rs/zerolog"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// encRekeyRetryMax is WA Web's retry cap for a retry request
// (WAWebPostMessageHighRetryCountMetric.MAX_RETRY).
const encRekeyRetryMax = 5

var errGroupRekeyDecrypt = fmt.Errorf("meowcaller: decrypt group rekey")

// requestGroupEpochRetry asks the device whose enc_rekey we couldn't decrypt
// to send it again, as WA Web does when a group call key fails to decrypt.
func (e *engine) requestGroupEpochRetry(node *waBinary.Node, envelope *signaling.CallControlEnvelope) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/stanza/index.json#L4345-L4409
	count := 1
	if enc, ok := envelope.Action.GetOptionalChildByTag("enc"); ok {
		count = enc.AttrGetter().OptionalInt("count") + 1
	}
	log := e.c.log.With().Str("call_id", envelope.CallID).Stringer("device_jid", envelope.From).Int("count", count).Logger()
	if count > encRekeyRetryMax {
		log.Warn().Msg("group rekey still undecryptable after the retry cap; not asking again")
		return
	}
	receipt, err := signaling.BuildEncRekeyRetryReceipt(signaling.EncRekeyRetry{
		From:           envelope.From,
		ID:             node.AttrGetter().OptionalString("id"),
		CallID:         envelope.CallID,
		CallCreator:    envelope.CallCreator,
		Count:          count,
		RegistrationID: e.c.wa.Store.RegistrationID,
	})
	if err != nil {
		log.Warn().Err(err).Msg("build group rekey retry receipt failed")
		return
	}
	if err = e.transmitCallNode(context.Background(), receipt); err != nil {
		log.Warn().Err(err).Msg("send group rekey retry receipt failed")
		return
	}
	log.Info().Msg("asked for the group rekey again")
}

// onEncRekeyRetry answers a device that couldn't decrypt our enc_rekey: like
// WA Web's handleMessageRetryRequest, it drops our Signal session with that
// device if its registration id changed (so the epoch goes out as a fresh
// pkmsg), then resends the call's current epoch to that device alone.
func (e *engine) onEncRekeyRetry(node *waBinary.Node) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/stanza/index.json#L4345-L4409
	retry, err := signaling.ParseEncRekeyRetryReceipt(node)
	if err != nil {
		e.c.log.Warn().Err(err).Msg("parse group rekey retry receipt failed")
		return
	}
	log := e.c.log.With().Str("call_id", retry.CallID).Stringer("device_jid", retry.From).Int("count", retry.Count).Logger()
	if retry.Count >= encRekeyRetryMax {
		log.Warn().Msg("refusing group rekey retry past the retry cap")
		return
	}
	e.mu.Lock()
	m := e.calls[retry.CallID]
	if m == nil || !m.group || !m.hasGroupEpoch || len(m.groupRawEpoch) != 32 {
		e.mu.Unlock()
		log.Debug().Msg("group rekey retry for a call without a key epoch; ignoring")
		return
	}
	rawKey := append([]byte(nil), m.groupRawEpoch...)
	transactionID := m.groupEpochTxID
	creator := m.creator
	e.mu.Unlock()
	defer clear(rawKey)
	if creator.IsEmpty() {
		creator = retry.CallCreator
	}

	ctx := context.Background()
	target := retry.From
	if target.Server == types.DefaultUserServer {
		if lid, lidErr := e.c.wa.Store.LIDs.GetLIDForPN(ctx, target); lidErr == nil && !lid.IsEmpty() {
			target = lid
		}
	}
	e.dropStaleSession(ctx, target, retry.RegistrationID, log)

	ciphertext, encType, needIdentity, err := encryptCallKeyForDevice(ctx, e.c.wa, target, rawKey)
	if err != nil {
		log.Warn().Err(err).Msg("encrypt group epoch for retry failed")
		return
	}
	var deviceIdentity []byte
	if needIdentity {
		if deviceIdentity, err = proto.Marshal(e.c.wa.Store.Account); err != nil {
			log.Warn().Err(err).Msg("marshal device identity failed")
			return
		}
	}
	// ASSUMPTION: the resent enc carries the requester's count, as a resent
	// message's enc does; WA's make_and_send_rekey_msg logs a retry count but
	// its wire form isn't confirmed.
	rekey, err := signaling.BuildGroupEncRekey(signaling.GroupEncRekeyParams{
		CallID: retry.CallID, To: target, CallCreator: creator,
		TransactionID: transactionID, RequestID: e.nextCallNodeID(),
		DeviceKey:      signaling.OfferDeviceKey{DeviceJid: target, Ciphertext: ciphertext, EncType: encType},
		DeviceIdentity: deviceIdentity,
		RetryCount:     retry.Count,
	})
	if err != nil {
		log.Warn().Err(err).Msg("build group epoch retry failed")
		return
	}
	if err = e.transmitCallNode(ctx, rekey); err != nil {
		log.Warn().Err(err).Msg("send group epoch retry failed")
		return
	}
	log.Info().Uint32("transaction_id", transactionID).Str("enc_type", encType).Msg("resent group epoch")
}

// dropStaleSession deletes our Signal session with dev when it was set up
// with a different registration id than dev now reports, as WA Web's
// updateLocalSignalSession does, so the next encryption starts a new one.
func (e *engine) dropStaleSession(ctx context.Context, dev types.JID, registrationID uint32, log zerolog.Logger) {
	// Source of truth: https://github.com/oxidezap/whatspec/blob/46f630441389e703e04ff3fc012192cf80fdd4ba/generated/bundles.lock.json#L1-L4
	address := dev.SignalAddress()
	has, err := e.c.wa.Store.ContainsSession(ctx, address)
	if err != nil || !has {
		return
	}
	session, err := e.c.wa.Store.LoadSession(ctx, address)
	if err != nil {
		log.Debug().Err(err).Msg("load signal session for retry failed")
		return
	}
	if session.SessionState().RemoteRegistrationID() == registrationID {
		return
	}
	if err = e.c.wa.Store.Sessions.DeleteSession(ctx, address.String()); err != nil {
		log.Warn().Err(err).Msg("delete stale signal session failed")
		return
	}
	log.Debug().Msg("dropped signal session after a registration id change")
}
