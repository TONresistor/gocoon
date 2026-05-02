package cocoon

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCorrelatorBasicDeliver(t *testing.T) {
	c := newCorrelator()
	id := QueryID(1)
	answer, errCh, err := c.register(id, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !c.deliver(id, []byte("ok")) {
		t.Errorf("deliver returned false")
	}
	select {
	case got := <-answer:
		if string(got) != "ok" {
			t.Errorf("got %q", got)
		}
	case e := <-errCh:
		t.Errorf("unexpected error: %v", e)
	case <-time.After(time.Second):
		t.Errorf("timeout")
	}
}

func TestCorrelatorDuplicateID(t *testing.T) {
	c := newCorrelator()
	id := QueryID(2)
	if _, _, err := c.register(id, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.register(id, time.Second); err == nil {
		t.Errorf("expected error on duplicate registration")
	}
}

func TestCorrelatorFail(t *testing.T) {
	c := newCorrelator()
	id := QueryID(3)
	_, errCh, _ := c.register(id, time.Second)
	if !c.fail(id, ErrRequestRejected) {
		t.Errorf("fail returned false")
	}
	select {
	case e := <-errCh:
		if !errors.Is(e, ErrRequestRejected) {
			t.Errorf("unexpected error: %v", e)
		}
	case <-time.After(time.Second):
		t.Errorf("timeout")
	}
}

func TestCorrelatorGCExpired(t *testing.T) {
	c := newCorrelator()
	id := QueryID(4)
	_, errCh, _ := c.register(id, time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if got := c.gcExpired(time.Now()); got != 1 {
		t.Errorf("gcExpired returned %d, want 1", got)
	}
	select {
	case e := <-errCh:
		if !errors.Is(e, ErrRequestTimeout) {
			t.Errorf("unexpected error: %v", e)
		}
	case <-time.After(100 * time.Millisecond):
		t.Errorf("timeout waiting for gc'd error")
	}
}

func TestCorrelatorFailAll(t *testing.T) {
	c := newCorrelator()
	chs := make([]<-chan error, 3)
	for i := 0; i < 3; i++ {
		_, errCh, _ := c.register(QueryID(i+10), time.Second)
		chs[i] = errCh
	}
	c.failAll(ErrAlreadyClosed)
	for i, ch := range chs {
		select {
		case e := <-ch:
			if !errors.Is(e, ErrAlreadyClosed) {
				t.Errorf("ch[%d]: got %v", i, e)
			}
		case <-time.After(time.Second):
			t.Errorf("ch[%d] timeout", i)
		}
	}
	if _, _, err := c.register(QueryID(99), time.Second); err == nil {
		t.Errorf("expected error on closed correlator")
	}
}

func TestCorrelatorConcurrent(t *testing.T) {
	c := newCorrelator()
	var wg sync.WaitGroup
	const n = 100
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := QueryID(i + 1000)
			answer, errCh, err := c.register(id, time.Second)
			if err != nil {
				return
			}
			c.deliver(id, []byte{byte(i)})
			select {
			case <-answer:
			case <-errCh:
			case <-time.After(time.Second):
				t.Errorf("i=%d timeout", i)
			}
		}(i)
	}
	wg.Wait()
}
