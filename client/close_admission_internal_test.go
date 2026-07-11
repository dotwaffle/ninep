package client

import "testing"

func TestConnRequestAdmissionClosesBeforeWait(t *testing.T) {
	c := &Conn{}
	if !c.beginCall() {
		t.Fatal("first request was not admitted")
	}
	c.endCall()

	c.closeAdmission()
	if c.beginCall() {
		c.endCall()
		t.Fatal("request admitted after shutdown closed admission")
	}

	done := make(chan struct{})
	go func() {
		c.callerWG.Wait()
		close(done)
	}()
	<-done
}
