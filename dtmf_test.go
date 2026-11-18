package meowcaller

import (
	"errors"
	"testing"
)

func TestDTMFOnlyForBusinessAPICalls(t *testing.T) {
	c := &Call{}
	if err := c.SendDTMF('5'); !errors.Is(err, ErrDTMFNotSupported) {
		t.Fatalf("got %v, want ErrDTMFNotSupported", err)
	}
	if err := c.SendDTMF('x'); err == nil {
		t.Fatal("accepted a tone that isn't on a keypad")
	}
	c.setPeerPlatform("capi")
	if !c.BusinessAPICall() {
		t.Fatal("capi peer not seen as a business call")
	}
}
