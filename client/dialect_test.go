package client

import (
	"errors"
	"testing"
)

// TestDialect_RequireL_OnUConn: requireDialect(L) on a .u Conn returns
// ErrNotSupported wrapped with op context.
func TestDialect_RequireL_OnUConn(t *testing.T) {
	t.Parallel()
	c := &Conn{dialect: protocolU}
	err := c.requireDialect(protocolL, "Lopen")
	if err == nil {
		t.Fatal("requireDialect returned nil, want ErrNotSupported")
	}
	if !errors.Is(err, ErrNotSupported) {
		t.Errorf("err = %v, want ErrNotSupported", err)
	}
}

// TestDialect_RequireU_OnLConn: requireDialect(U) on a .L Conn returns
// ErrNotSupported.
func TestDialect_RequireU_OnLConn(t *testing.T) {
	t.Parallel()
	c := &Conn{dialect: protocolL}
	err := c.requireDialect(protocolU, "Open")
	if err == nil {
		t.Fatal("requireDialect returned nil, want ErrNotSupported")
	}
	if !errors.Is(err, ErrNotSupported) {
		t.Errorf("err = %v, want ErrNotSupported", err)
	}
}

// TestDialect_Match: requireDialect(c.dialect) returns nil.
func TestDialect_Match(t *testing.T) {
	t.Parallel()
	cL := &Conn{dialect: protocolL}
	if err := cL.requireDialect(protocolL, "Lopen"); err != nil {
		t.Errorf("requireDialect L/L = %v, want nil", err)
	}

	cU := &Conn{dialect: protocolU}
	if err := cU.requireDialect(protocolU, "Open"); err != nil {
		t.Errorf("requireDialect U/U = %v, want nil", err)
	}
}
