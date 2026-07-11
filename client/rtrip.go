package client

import (
	"context"
	"fmt"

	"github.com/dotwaffle/ninep/proto"
)

// rtrip performs one request/response exchange and asserts the response
// to T. Server errors (Rlerror/Rerror) surface as *Error; a response of
// any other type is returned to the message cache and reported as a
// type-mismatch error. On success the caller owns the returned message
// and MUST route it back through putCachedRMsg after extracting what it
// needs -- copying out any field that aliases pooled storage (Rread.Data,
// Rwalk.QIDs, Rreaddir.Data) first.
func rtrip[T proto.Message](ctx context.Context, c *Conn, req proto.Message) (T, error) {
	var zero T
	resp, err := c.roundTrip(ctx, req)
	if err != nil {
		return zero, err
	}
	if err := toError(resp); err != nil {
		return zero, err
	}
	r, ok := resp.(T)
	if !ok {
		err := fmt.Errorf("client: expected %v, got %v", zero.Type(), resp.Type())
		putCachedRMsg(resp)
		return zero, err
	}
	return r, nil
}
