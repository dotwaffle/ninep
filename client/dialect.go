package client

import "fmt"

// requireDialect returns nil if c.dialect matches want, otherwise an error
// wrapping ErrNotSupported with context about which op was invoked and what
// dialect the Conn actually negotiated. Called at the entry of every
// dialect-gated op method per D-21 (.planning/phases/19/19-CONTEXT.md).
//
// The check is a single inline compare — no reflection, no map lookup —
// so it stays off the critical path for the common (matching-dialect)
// case. The cost of the mismatch path is the fmt.Errorf allocation, which
// is amortized across a user-surface error return anyway.
func (c *Conn) requireDialect(want protocol, op string) error {
	if c.dialect == want {
		return nil
	}
	return fmt.Errorf("%w: %s requires %s, Conn negotiated %s",
		ErrNotSupported, op, want, c.dialect)
}
