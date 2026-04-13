package server

import (
	"context"
	"testing"

	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/proto/p9l"
	"github.com/dotwaffle/ninep/proto/p9u"
)

// orderMiddleware creates a middleware that appends its id to the order slice
// before and after calling next.
func orderMiddleware(id string, order *[]string) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
			*order = append(*order, id+"-before")
			resp := next(ctx, tag, msg)
			*order = append(*order, id+"-after")
			return resp
		}
	}
}

func TestMiddlewareChainIdentity(t *testing.T) {
	t.Parallel()

	// chain with zero middleware returns inner handler result unchanged.
	inner := func(_ context.Context, _ proto.Tag, _ proto.Message) proto.Message {
		return &proto.Rversion{Msize: 42}
	}

	h := chain(inner, nil)
	resp := h(context.Background(), 0, &proto.Tversion{})
	rv, ok := resp.(*proto.Rversion)
	if !ok {
		t.Fatalf("expected *proto.Rversion, got %T", resp)
	}
	if rv.Msize != 42 {
		t.Fatalf("expected Msize=42, got %d", rv.Msize)
	}
}

func TestMiddlewareChainSingle(t *testing.T) {
	t.Parallel()

	// chain with one middleware wraps inner.
	var order []string
	inner := func(_ context.Context, _ proto.Tag, _ proto.Message) proto.Message {
		order = append(order, "inner")
		return &proto.Rversion{Msize: 99}
	}

	mw := orderMiddleware("A", &order)
	h := chain(inner, []Middleware{mw})
	resp := h(context.Background(), 0, &proto.Tversion{})

	want := []string{"A-before", "inner", "A-after"}
	if len(order) != len(want) {
		t.Fatalf("order length: got %d, want %d", len(order), len(want))
	}
	for i, v := range want {
		if order[i] != v {
			t.Fatalf("order[%d]: got %q, want %q", i, order[i], v)
		}
	}

	rv := resp.(*proto.Rversion)
	if rv.Msize != 99 {
		t.Fatalf("expected Msize=99, got %d", rv.Msize)
	}
}

func TestMiddlewareChainOrdering(t *testing.T) {
	t.Parallel()

	// chain with two middleware executes outer-first order.
	// First added = outermost = first to run.
	var order []string
	inner := func(_ context.Context, _ proto.Tag, _ proto.Message) proto.Message {
		order = append(order, "inner")
		return &proto.Rversion{}
	}

	mwA := orderMiddleware("A", &order)
	mwB := orderMiddleware("B", &order)

	h := chain(inner, []Middleware{mwA, mwB})
	h(context.Background(), 0, &proto.Tversion{})

	// A is outermost (first added), B is inner.
	// Execution: A-before -> B-before -> inner -> B-after -> A-after
	want := []string{"A-before", "B-before", "inner", "B-after", "A-after"}
	if len(order) != len(want) {
		t.Fatalf("order length: got %d, want %d", len(order), len(want))
	}
	for i, v := range want {
		if order[i] != v {
			t.Fatalf("order[%d]: got %q, want %q", i, order[i], v)
		}
	}
}

func TestMiddlewareChainShortCircuit(t *testing.T) {
	t.Parallel()

	// Middleware can short-circuit by not calling next.
	var innerCalled bool
	inner := func(_ context.Context, _ proto.Tag, _ proto.Message) proto.Message {
		innerCalled = true
		return &proto.Rversion{}
	}

	shortCircuit := func(_ Handler) Handler {
		return func(_ context.Context, _ proto.Tag, _ proto.Message) proto.Message {
			return &proto.Rversion{Msize: 1}
		}
	}

	h := chain(inner, []Middleware{shortCircuit})
	resp := h(context.Background(), 0, &proto.Tversion{})

	if innerCalled {
		t.Fatal("inner should not have been called")
	}
	rv := resp.(*proto.Rversion)
	if rv.Msize != 1 {
		t.Fatalf("expected Msize=1, got %d", rv.Msize)
	}
}

func TestMiddlewareChainPanicRecovery(t *testing.T) {
	t.Parallel()

	// Panic inside middleware chain is recoverable by caller.
	inner := func(_ context.Context, _ proto.Tag, _ proto.Message) proto.Message {
		return &proto.Rversion{}
	}

	panicker := func(next Handler) Handler {
		return func(ctx context.Context, tag proto.Tag, msg proto.Message) proto.Message {
			panic("middleware panic")
		}
	}

	h := chain(inner, []Middleware{panicker})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic to propagate")
		}
		if r != "middleware panic" {
			t.Fatalf("unexpected panic value: %v", r)
		}
	}()

	h(context.Background(), 0, &proto.Tversion{})
}

func TestIsErrorResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  proto.Message
		want bool
	}{
		{
			name: "Rlerror is error",
			msg:  &p9l.Rlerror{Ecode: proto.EIO},
			want: true,
		},
		{
			name: "Rerror is error",
			msg:  &p9u.Rerror{Ename: "error", Errno: proto.EIO},
			want: true,
		},
		{
			name: "Rversion is not error",
			msg:  &proto.Rversion{},
			want: false,
		},
		{
			name: "Rwalk is not error",
			msg:  &proto.Rwalk{},
			want: false,
		},
		{
			name: "Rattach is not error",
			msg:  &proto.Rattach{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isErrorResponse(tt.msg)
			if got != tt.want {
				t.Fatalf("isErrorResponse(%T): got %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestFidFromMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		msg     proto.Message
		wantFid proto.Fid
		wantOK  bool
	}{
		{
			name:    "Tattach",
			msg:     &proto.Tattach{Fid: 10},
			wantFid: 10,
			wantOK:  true,
		},
		{
			name:    "Twalk",
			msg:     &proto.Twalk{Fid: 20},
			wantFid: 20,
			wantOK:  true,
		},
		{
			name:    "Tclunk",
			msg:     &proto.Tclunk{Fid: 30},
			wantFid: 30,
			wantOK:  true,
		},
		{
			name:    "Tread",
			msg:     &proto.Tread{Fid: 40},
			wantFid: 40,
			wantOK:  true,
		},
		{
			name:    "Twrite",
			msg:     &proto.Twrite{Fid: 50},
			wantFid: 50,
			wantOK:  true,
		},
		{
			name:    "Tremove",
			msg:     &proto.Tremove{Fid: 60},
			wantFid: 60,
			wantOK:  true,
		},
		{
			name:    "Tlopen",
			msg:     &p9l.Tlopen{Fid: 70},
			wantFid: 70,
			wantOK:  true,
		},
		{
			name:    "Tgetattr",
			msg:     &p9l.Tgetattr{Fid: 80},
			wantFid: 80,
			wantOK:  true,
		},
		{
			name:    "Treaddir",
			msg:     &p9l.Treaddir{Fid: 90},
			wantFid: 90,
			wantOK:  true,
		},
		{
			name:    "Tstatfs",
			msg:     &p9l.Tstatfs{Fid: 100},
			wantFid: 100,
			wantOK:  true,
		},
		{
			name:    "Rversion has no fid",
			msg:     &proto.Rversion{},
			wantFid: 0,
			wantOK:  false,
		},
		{
			name:    "Rwalk has no fid",
			msg:     &proto.Rwalk{},
			wantFid: 0,
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fid, ok := fidFromMessage(tt.msg)
			if ok != tt.wantOK {
				t.Fatalf("fidFromMessage(%T): ok=%v, want %v", tt.msg, ok, tt.wantOK)
			}
			if fid != tt.wantFid {
				t.Fatalf("fidFromMessage(%T): fid=%d, want %d", tt.msg, fid, tt.wantFid)
			}
		})
	}
}
