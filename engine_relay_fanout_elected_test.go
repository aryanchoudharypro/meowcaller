package meowcaller

import (
	"net"
	"testing"
	"time"

	"github.com/purpshell/meowcaller/relay"
)

type fakeRelay struct {
	sock *net.UDPConn
	ch   *relay.RelayMediaChannel
}

func newFakeRelays(t *testing.T, n int) []fakeRelay {
	t.Helper()
	var out []fakeRelay
	for i := 0; i < n; i++ {
		sock, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sock.Close() })
		ch, err := relay.ConnectRelayUDP(sock.LocalAddr().(*net.UDPAddr))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fakeRelay{sock: sock, ch: ch})
	}
	return out
}

// got reports whether the relay received a datagram within d, and its address.
func (r fakeRelay) got(d time.Duration) (*net.UDPAddr, bool) {
	buf := make([]byte, 2048)
	_ = r.sock.SetReadDeadline(time.Now().Add(d))
	_, from, err := r.sock.ReadFromUDP(buf)
	return from, err == nil
}

func TestSendElectedFollowsTheRelayThePeersMediaArrivesFrom(t *testing.T) {
	relays := newFakeRelays(t, 3)
	chans := []*relay.RelayMediaChannel{relays[0].ch, relays[1].ch, relays[2].ch}
	f := newRelayFanout(chans, make([][]byte, 3), []string{"a", "b", "c"})
	defer f.Close()
	var elected []string
	f.onElected = func(name string) { elected = append(elected, name) }

	// Nothing from the peer yet: broadcast, and learn each channel's address.
	if _, err := f.SendElected([]byte{0x80, 97, 0, 1}); err != nil {
		t.Fatal(err)
	}
	addrs := make([]*net.UDPAddr, 3)
	for i, r := range relays {
		from, ok := r.got(time.Second)
		if !ok {
			t.Fatalf("relay %d missed the broadcast", i)
		}
		addrs[i] = from
	}

	// The peer's RTP arrives over relay b.
	if _, err := relays[1].sock.WriteToUDP([]byte{0x80, 120, 0, 9, 0, 0, 0, 0, 0, 0, 0, 1}, addrs[1]); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	if _, err := f.Recv(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := f.SendElected([]byte{0x80, 97, 0, 2}); err != nil {
		t.Fatal(err)
	}
	if _, ok := relays[1].got(time.Second); !ok {
		t.Fatal("the elected relay did not get the packet")
	}
	for _, i := range []int{0, 2} {
		if _, ok := relays[i].got(150 * time.Millisecond); ok {
			t.Fatalf("relay %d got a packet meant for the elected relay only", i)
		}
	}
	if len(elected) != 1 || elected[0] != "b" {
		t.Fatalf("elected %v, want [b]", elected)
	}
}
