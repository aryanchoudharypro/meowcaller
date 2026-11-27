package p2p

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/purpshell/meowcaller/signaling"
)

// TestLoopbackDataChannel opens a caller and a callee in one process, wired
// through their type 13 transports, and sends one packet each way.
func TestLoopbackDataChannel(t *testing.T) {
	for _, peerIsWeb := range []bool{true, false} {
		t.Run(map[bool]string{true: "web_peer", false: "native_peer"}[peerIsWeb], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var caller, callee *Conn
			toCallee := make(chan *signaling.WebP2PTransport, 64)
			toCaller := make(chan *signaling.WebP2PTransport, 64)
			base := Config{CallID: "CALL", SkipFallbackSTUN: true, IncludeLoopback: true, PeerIsWeb: true}
			cc := base
			cc.IsCaller = true
			cc.SendTransport = func(tr *signaling.WebP2PTransport) error { toCallee <- tr; return nil }
			ce := base
			// A callee is the DTLS server toward either kind of peer, and a
			// web caller is the client, so both pairings must open.
			ce.PeerIsWeb = peerIsWeb
			ce.SendTransport = func(tr *signaling.WebP2PTransport) error { toCaller <- tr; return nil }
			var err error
			if caller, err = Dial(ctx, cc); err != nil {
				t.Fatal(err)
			}
			defer caller.Close()
			if callee, err = Dial(ctx, ce); err != nil {
				t.Fatal(err)
			}
			defer callee.Close()
			pump := func(ch chan *signaling.WebP2PTransport, dst *Conn) {
				for {
					select {
					case tr := <-ch:
						_ = dst.AddRemote(tr)
					case <-ctx.Done():
						return
					}
				}
			}
			go pump(toCallee, callee)
			go pump(toCaller, caller)
			for _, c := range []*Conn{caller, callee} {
				select {
				case <-c.Opened():
				case <-c.Done():
					t.Fatalf("closed: %v", c.Err())
				case <-ctx.Done():
					t.Fatal("timeout waiting for open")
				}
			}
			if err := caller.Send([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			got, err := callee.Recv(ctx)
			if err != nil || !bytes.Equal(got, []byte("ping")) {
				t.Fatalf("callee got %q, %v", got, err)
			}
			if err := callee.Send([]byte("pong")); err != nil {
				t.Fatal(err)
			}
			got, err = caller.Recv(ctx)
			if err != nil || !bytes.Equal(got, []byte("pong")) {
				t.Fatalf("caller got %q, %v", got, err)
			}
		})
	}
}
