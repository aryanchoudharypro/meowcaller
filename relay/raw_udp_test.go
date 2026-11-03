package relay

import (
	"net"
	"testing"
	"time"
)

func TestConnectRelayUDPSendRecv(t *testing.T) {
	relaySock, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer relaySock.Close()
	stranger, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer stranger.Close()

	ch, err := ConnectRelayUDP(relaySock.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	if !ch.IsRaw() {
		t.Fatal("expected a raw channel")
	}
	if _, err := ch.Send([]byte("allocate")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = relaySock.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from, err := relaySock.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "allocate" {
		t.Fatalf("relay got %q, %v", buf[:n], err)
	}
	// A datagram from anywhere but the relay is dropped.
	if _, err := stranger.WriteToUDP([]byte("noise"), from); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := relaySock.WriteToUDP([]byte("success"), from); err != nil {
		t.Fatal(err)
	}
	n, err = ch.Recv(buf)
	if err != nil || string(buf[:n]) != "success" {
		t.Fatalf("channel got %q, %v", buf[:n], err)
	}
}
