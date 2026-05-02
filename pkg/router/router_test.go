package router

import (
	"context"
	"testing"
)

func TestDefaultDialerRejectsEmptyAddr(t *testing.T) {
	d := &DefaultDialer{}
	if _, err := d.DialProxy(context.Background(), ""); err == nil {
		t.Errorf("expected error on empty addr")
	}
}
