package meowcaller

import (
	"errors"
	"fmt"
	"sync"

	"github.com/purpshell/meowcaller/relay"
)

// relayFanout runs one logical media channel across every usable relay in the
// offer, mirroring the reference implementation's bind-to-all model: outbound
// packets broadcast to every relay, inbound merges from all of them.
//
// Phones run client-side relay election shortly after accept and move their
// media to whichever offered relay they measured closest — which is frequently
// not the first one listed. A callee bound to a single relay goes deaf at that
// moment (observed live as 5–8 comfort-noise packets then permanent silence;
// issues #21 and #22). Being connected everywhere makes the election
// outcome irrelevant. WhatsApp Web callers never migrate, which is why
// single-relay binding appeared to work when tested from web.
type relayFanout struct {
	chans  []*relay.RelayMediaChannel
	allocs [][]byte
	names  []string

	packets   chan []byte
	done      chan struct{}
	closeOnce sync.Once

	mu     sync.Mutex
	closed bool
	live   int
}

// fanoutBacklog bounds merged inbound packets awaiting Recv. At 60ms audio
// cadence even a multi-relay burst sits far below this; overflow drops rather
// than blocks so one slow consumer can't stall the per-relay readers.
const fanoutBacklog = 256

var errFanoutClosed = errors.New("meowcaller: relay fanout closed")

// newRelayFanout takes ownership of the channels and their matching per-relay
// allocate payloads, and starts one reader per channel.
func newRelayFanout(chans []*relay.RelayMediaChannel, allocs [][]byte, names []string) *relayFanout {
	// Source of truth: https://github.com/JotaDev66/WaCalls/blob/edeb31f0427aba896639db503153b777a405eccf/internal/voip/transport/sctprelay.go#L105-L127
	f := &relayFanout{
		chans:   chans,
		allocs:  allocs,
		names:   names,
		packets: make(chan []byte, fanoutBacklog),
		done:    make(chan struct{}),
		live:    len(chans),
	}
	for _, ch := range chans {
		go f.readLoop(ch)
	}
	return f
}

func (f *relayFanout) readLoop(ch *relay.RelayMediaChannel) {
	buf := make([]byte, 2048)
	for {
		n, err := ch.Recv(buf)
		if err != nil {
			f.readerExited()
			return
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		select {
		case f.packets <- pkt:
		case <-f.done:
			return
		default:
			// Backlog full. Live media prefers a dropped packet over a stalled
			// reader; audio recovers next frame, video on the next keyframe.
		}
	}
}

// readerExited counts a relay connection that died. The fanout stays open with
// none left: the call's connection watch redials them, or ends the call.
func (f *relayFanout) readerExited() {
	f.mu.Lock()
	f.live--
	f.mu.Unlock()
}

// Send broadcasts one packet to every relay, succeeding if at least one relay
// accepted it. The peer reads our stream from whichever relay it elected, and
// the others simply discard.
func (f *relayFanout) Send(data []byte) (int, error) {
	// Source of truth: https://github.com/JotaDev66/WaCalls/blob/edeb31f0427aba896639db503153b777a405eccf/internal/voip/transport/sctprelay.go#L345-L355
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, errFanoutClosed
	}
	chans := f.chans
	f.mu.Unlock()

	sent := false
	var lastErr error
	for _, ch := range chans {
		if _, err := ch.Send(data); err != nil {
			lastErr = err
			continue
		}
		sent = true
	}
	if !sent {
		if lastErr == nil {
			lastErr = errFanoutClosed
		}
		return 0, lastErr
	}
	return len(data), nil
}

// Recv pops the next packet from any relay into buf, blocking like the
// single-channel Recv it replaces.
func (f *relayFanout) Recv(buf []byte) (int, error) {
	select {
	case pkt := <-f.packets:
		n := copy(buf, pkt)
		return n, nil
	case <-f.done:
		// Drain anything already merged before reporting closure.
		select {
		case pkt := <-f.packets:
			n := copy(buf, pkt)
			return n, nil
		default:
			return 0, errFanoutClosed
		}
	}
}

// ResendAllocates re-sends each relay its own allocate payload. Allocates embed
// the relay-specific token and XOR-encoded endpoint, so they must never be
// broadcast: relay A's allocate is meaningless to relay B.
func (f *relayFanout) ResendAllocates() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errFanoutClosed
	}
	chans, allocs := f.chans, f.allocs
	f.mu.Unlock()

	sent := false
	var lastErr error
	for i, ch := range chans {
		if i >= len(allocs) || allocs[i] == nil {
			continue
		}
		if _, err := ch.Send(allocs[i]); err != nil {
			lastErr = err
			continue
		}
		sent = true
	}
	if !sent && lastErr != nil {
		return lastErr
	}
	return nil
}

// PrimarySend writes to the first relay only. The experimental group-call path
// keeps its original single-relay semantics; only 1:1 calls fan out.
func (f *relayFanout) PrimarySend(data []byte) (int, error) {
	f.mu.Lock()
	if f.closed || len(f.chans) == 0 {
		f.mu.Unlock()
		return 0, errFanoutClosed
	}
	primary := f.chans[0]
	f.mu.Unlock()
	return primary.Send(data)
}

// Rebind collapses the fanout onto one relay and retires every other
// connection. With a replacement channel that channel becomes the relay;
// without one the already-connected relay called name is kept. A group
// allocation lives on exactly one relay, and the server may hand out a relay
// set the 1:1 call was never bound to.
func (f *relayFanout) Rebind(name string, replacement *relay.RelayMediaChannel) error {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d9f78b806f1f4ca80c8008caa5846e5d542c2c55/wacore/src/voip/driver.rs#L838-L861
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		if replacement != nil {
			_ = replacement.Close()
		}
		return errFanoutClosed
	}
	keep := replacement
	var retired []*relay.RelayMediaChannel
	for i, ch := range f.chans {
		if keep == nil && i < len(f.names) && f.names[i] == name {
			keep = ch
			continue
		}
		retired = append(retired, ch)
	}
	if keep == nil {
		f.mu.Unlock()
		return fmt.Errorf("meowcaller: relay %s is not connected", name)
	}
	if replacement != nil {
		f.live++
	}
	// Fresh slices: Send and ResendAllocates iterate snapshots taken under mu.
	f.chans = []*relay.RelayMediaChannel{keep}
	f.allocs = [][]byte{nil}
	f.names = []string{name}
	f.mu.Unlock()

	if replacement != nil {
		go f.readLoop(replacement)
	}
	for _, ch := range retired {
		_ = ch.Close()
	}
	return nil
}

// snapshot returns the bound relays' names and allocate payloads, index-aligned.
func (f *relayFanout) snapshot() ([]string, [][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.names...), append([][]byte(nil), f.allocs...)
}

// replace swaps the connection bound to relay name for a freshly dialed one,
// with its allocate payload (nil keeps none), and retires the old connection.
func (f *relayFanout) replace(name string, ch *relay.RelayMediaChannel, allocate []byte) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errFanoutClosed
	}
	index := -1
	for i, bound := range f.names {
		if bound == name {
			index = i
			break
		}
	}
	if index < 0 {
		f.mu.Unlock()
		return fmt.Errorf("meowcaller: relay %s is not bound", name)
	}
	chans := append([]*relay.RelayMediaChannel(nil), f.chans...)
	allocs := append([][]byte(nil), f.allocs...)
	old := chans[index]
	chans[index] = ch
	if index < len(allocs) {
		allocs[index] = allocate
	}
	// Fresh slices: Send and ResendAllocates iterate snapshots taken under mu.
	f.chans, f.allocs = chans, allocs
	f.live++
	f.mu.Unlock()
	go f.readLoop(ch)
	_ = old.Close()
	return nil
}

func (f *relayFanout) Close() error {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.closed = true
		chans := f.chans
		f.mu.Unlock()
		close(f.done)
		for _, ch := range chans {
			_ = ch.Close()
		}
	})
	return nil
}

// rtpReplayFilter drops RTP packets already seen on (ssrc, seq). With every
// relay bridging the same session, one packet can arrive once per relay;
// duplicates reaching the playout buffer read as a zero-timestamp delta, which
// resets it and audibly glitches the stream. A 1024-slot ring per SSRC covers
// ~61s of 60ms audio frames — far beyond any realistic reorder window.
type rtpReplayFilter struct {
	streams map[uint32]*replayRing
}

type replayRing struct {
	seen   [1024 / 64]uint64
	seq    [1024]uint16
	primed bool
}

func newRtpReplayFilter() *rtpReplayFilter {
	return &rtpReplayFilter{streams: make(map[uint32]*replayRing)}
}

// Duplicate records (ssrc, seq) and reports whether it was already seen.
func (r *rtpReplayFilter) Duplicate(ssrc uint32, seq uint16) bool {
	ring, ok := r.streams[ssrc]
	if !ok {
		ring = &replayRing{}
		r.streams[ssrc] = ring
	}
	slot := int(seq) % 1024
	word, bit := slot/64, uint(slot%64)
	if ring.primed && ring.seen[word]&(1<<bit) != 0 && ring.seq[slot] == seq {
		return true
	}
	ring.primed = true
	ring.seen[word] |= 1 << bit
	ring.seq[slot] = seq
	return false
}
