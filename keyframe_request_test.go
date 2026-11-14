package meowcaller

import "testing"

func TestRequestPeerVideoKeyframeIsTakenOnceForItsStream(t *testing.T) {
	c := &Call{}
	if c.takePeerKeyframeRequest(7) {
		t.Fatal("nothing was requested")
	}
	c.RequestPeerVideoKeyframe(7)
	if c.takePeerKeyframeRequest(8) {
		t.Fatal("a request for stream 7 went out on stream 8")
	}
	if !c.takePeerKeyframeRequest(7) {
		t.Fatal("the request for stream 7 was lost")
	}
	if c.takePeerKeyframeRequest(7) {
		t.Fatal("one request sent two PLIs")
	}
	c.RequestPeerVideoKeyframe(0)
	if !c.takePeerKeyframeRequest(9) {
		t.Fatal("a request for any stream must go out on the next one")
	}
	var none *Call
	if none.takePeerKeyframeRequest(1) {
		t.Fatal("a nil call has no requests")
	}
}
