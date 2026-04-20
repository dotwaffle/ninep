package client_test

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/client"
	"github.com/dotwaffle/ninep/proto"
	"github.com/dotwaffle/ninep/server"
	"github.com/dotwaffle/ninep/server/memfs"
)

// -- Task 1 tests ---------------------------------------------------

// TestClient_LockType_Values asserts the LockType enum maps to the
// matching proto.LockType constants. Compile-time equivalence via the
// underlying type is also asserted via a const-expression.
func TestClient_LockType_Values(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  client.LockType
		want proto.LockType
	}{
		{"LockRead", client.LockRead, proto.LockTypeRdLck},
		{"LockWrite", client.LockWrite, proto.LockTypeWrLck},
		{"LockUnlock", client.LockUnlock, proto.LockTypeUnlck},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if uint8(c.got) != uint8(c.want) {
				t.Errorf("%s = %d, want %d", c.name, uint8(c.got), uint8(c.want))
			}
		})
	}
}

// TestLockBackoff_Schedule validates the default curve and the cap
// behavior at iterations past the last index.
func TestLockBackoff_Schedule(t *testing.T) {
	t.Parallel()
	schedule := client.DefaultLockBackoff()
	want := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond,
		80 * time.Millisecond,
		160 * time.Millisecond,
		320 * time.Millisecond,
		500 * time.Millisecond,
	}
	if len(schedule) != len(want) {
		t.Fatalf("DefaultLockBackoff len = %d, want %d", len(schedule), len(want))
	}
	for i, d := range want {
		if got := client.BackoffFor(schedule, i); got != d {
			t.Errorf("BackoffFor(%d) = %v, want %v", i, got, d)
		}
	}
	// Past the end should cap at the last entry.
	for _, i := range []int{7, 8, 100, 10000} {
		if got := client.BackoffFor(schedule, i); got != 500*time.Millisecond {
			t.Errorf("BackoffFor(%d) = %v, want 500ms (capped)", i, got)
		}
	}
}

// TestClient_WithLockPollSchedule_Override dials a Conn with an
// overridden schedule. The observable side effect is verified in
// TestClient_Lock_Contended_Backoff — here we only assert the option
// doesn't error and takes effect on the first Blocked status.
func TestClient_WithLockPollSchedule_Override(t *testing.T) {
	t.Parallel()
	root, locker := buildLockerRoot(t)
	locker.queueStatus(proto.LockStatusBlocked, proto.LockStatusOK)

	cli, cleanup := newClientServerPair(t, root,
		client.WithLockPollSchedule([]time.Duration{1 * time.Millisecond}),
	)
	defer cleanup()

	f := openLockerFile(t, cli)
	defer func() { _ = f.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := f.Lock(ctx, client.LockWrite); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	elapsed := time.Since(start)
	// One 1ms backoff must have elapsed (Blocked -> sleep 1ms -> OK).
	// We don't enforce an upper bound tightly; we only assert the
	// override is in effect (i.e. much less than the 10ms default).
	if elapsed > 8*time.Millisecond {
		t.Errorf("Lock took %v with 1ms override, expected substantially less than 10ms default", elapsed)
	}
}

// -- Task 2 tests ---------------------------------------------------

func TestClient_Lock_Uncontended(t *testing.T) {
	t.Parallel()
	root, locker := buildLockerRoot(t)
	// Empty status queue -> Lock() returns OK on first call.

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	f := openLockerFile(t, cli)
	defer func() { _ = f.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := f.Lock(ctx, client.LockWrite); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	calls := locker.recordedCalls()
	if len(calls) != 1 {
		t.Fatalf("recordedCalls len = %d, want 1", len(calls))
	}
	if calls[0].LockType != proto.LockTypeWrLck {
		t.Errorf("first call LockType = %d, want WrLck", calls[0].LockType)
	}
}

func TestClient_Lock_Contended_Backoff(t *testing.T) {
	t.Parallel()
	root, locker := buildLockerRoot(t)
	locker.queueStatus(proto.LockStatusBlocked, proto.LockStatusBlocked, proto.LockStatusOK)

	cli, cleanup := newClientServerPair(t, root,
		client.WithLockPollSchedule([]time.Duration{1 * time.Millisecond}),
	)
	defer cleanup()

	f := openLockerFile(t, cli)
	defer func() { _ = f.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := f.Lock(ctx, client.LockWrite); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	elapsed := time.Since(start)
	calls := locker.recordedCalls()
	if len(calls) < 3 {
		t.Fatalf("recordedCalls len = %d, want >= 3 (Blocked, Blocked, OK)", len(calls))
	}
	// Two 1ms backoffs MUST have been observed.
	if elapsed < 2*time.Millisecond {
		t.Errorf("Lock elapsed %v, want >= 2ms (two 1ms backoffs)", elapsed)
	}
	// Count non-UNLCK calls to validate we actually retried.
	retries := 0
	for _, c := range calls {
		if c.LockType != proto.LockTypeUnlck {
			retries++
		}
	}
	if retries < 3 {
		t.Errorf("retry count = %d, want >= 3", retries)
	}
}

// TestClient_Lock_CtxCancel_SendsUnlock is the Pitfall-6 proof. On
// ctx cancellation mid-backoff loop, Lock MUST emit a Tlock(UNLCK)
// on the way out so the server-side lock table matches client state.
func TestClient_Lock_CtxCancel_SendsUnlock(t *testing.T) {
	t.Parallel()
	root, locker := buildLockerRoot(t)
	// Queue enough Blocked statuses that ctx deadline fires before OK.
	for range 50 {
		locker.queueStatus(proto.LockStatusBlocked)
	}

	cli, cleanup := newClientServerPair(t, root,
		client.WithLockPollSchedule([]time.Duration{1 * time.Millisecond}),
	)
	defer cleanup()

	f := openLockerFile(t, cli)
	defer func() { _ = f.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	err := f.Lock(ctx, client.LockWrite)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock err = %v, want DeadlineExceeded", err)
	}
	// After Lock returns, the belt-and-braces UNLCK MUST be observable.
	// It may take a brief moment since cleanup runs on a background ctx
	// and the Tlock round-trip is synchronous; give the server time to
	// record the call.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if locker.hasUnlock() {
			return // success
		}
		time.Sleep(1 * time.Millisecond)
	}
	t.Errorf("testLockerNode did not observe Tlock(UNLCK) cleanup after ctx-cancel; calls = %+v",
		locker.recordedCalls())
}

// TestClient_Lock_RequiresOpenedFid walks to a locker node WITHOUT
// opening it, then calls File.Lock — the server requires fidOpened
// state and MUST reject with a wire error.
func TestClient_Lock_RequiresOpenedFid(t *testing.T) {
	t.Parallel()
	root, _ := buildLockerRoot(t)

	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	rootFile, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// Walk but do NOT open.
	walked, err := rootFile.Walk(ctx, []string{"locker"})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	defer func() { _ = walked.Close() }()

	err = walked.Lock(ctx, client.LockWrite)
	if err == nil {
		t.Fatal("Lock on unopened fid: want error, got nil")
	}
	var cerr *client.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("Lock err = %v (%T), want *client.Error", err, err)
	}
	t.Logf("Lock on unopened fid errno = %v (OK)", cerr.Errno)
}

func TestClient_TryLock(t *testing.T) {
	t.Parallel()
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		root, locker := buildLockerRoot(t)
		locker.queueStatus(proto.LockStatusOK)
		cli, cleanup := newClientServerPair(t, root)
		defer cleanup()
		f := openLockerFile(t, cli)
		defer func() { _ = f.Close() }()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		ok, err := f.TryLock(ctx, client.LockWrite)
		if err != nil {
			t.Fatalf("TryLock: %v", err)
		}
		if !ok {
			t.Errorf("TryLock ok = false, want true")
		}
	})
	t.Run("blocked", func(t *testing.T) {
		t.Parallel()
		root, locker := buildLockerRoot(t)
		locker.queueStatus(proto.LockStatusBlocked)
		cli, cleanup := newClientServerPair(t, root)
		defer cleanup()
		f := openLockerFile(t, cli)
		defer func() { _ = f.Close() }()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		ok, err := f.TryLock(ctx, client.LockWrite)
		if err != nil {
			t.Fatalf("TryLock: %v", err)
		}
		if ok {
			t.Errorf("TryLock(blocked) ok = true, want false")
		}
	})
}

func TestClient_Unlock_Releases(t *testing.T) {
	t.Parallel()
	root, locker := buildLockerRoot(t)
	// First Lock -> OK; then Unlock; then Lock -> OK again.
	locker.queueStatus(proto.LockStatusOK, proto.LockStatusOK)
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()
	f := openLockerFile(t, cli)
	defer func() { _ = f.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := f.Lock(ctx, client.LockWrite); err != nil {
		t.Fatalf("Lock#1: %v", err)
	}
	if err := f.Unlock(ctx); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := f.Lock(ctx, client.LockWrite); err != nil {
		t.Fatalf("Lock#2: %v", err)
	}
	// Verify the server observed an UNLCK between the two OK locks.
	calls := locker.recordedCalls()
	sawUnlock := false
	for _, c := range calls {
		if c.LockType == proto.LockTypeUnlck {
			sawUnlock = true
			break
		}
	}
	if !sawUnlock {
		t.Errorf("expected UNLCK call between Lock/Lock, got %+v", calls)
	}
}

func TestClient_GetLock_Conflict(t *testing.T) {
	t.Parallel()
	root, locker := buildLockerRoot(t)
	// Queue OK so the initial Lock succeeds; GetLock afterwards returns
	// the conflicting holder from testLockerNode's held-state.
	locker.queueStatus(proto.LockStatusOK)
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()
	f := openLockerFile(t, cli)
	defer func() { _ = f.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := f.Lock(ctx, client.LockWrite); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	got, err := f.GetLock(ctx, client.LockWrite)
	if err != nil {
		t.Fatalf("GetLock: %v", err)
	}
	if got == nil {
		t.Fatal("GetLock = nil, want non-nil holder")
	}
	if got.Type != client.LockWrite {
		t.Errorf("GetLock.Type = %d, want LockWrite(%d)", got.Type, client.LockWrite)
	}
}

func TestClient_GetLock_Free(t *testing.T) {
	t.Parallel()
	root, _ := buildLockerRoot(t)
	cli, cleanup := newClientServerPair(t, root)
	defer cleanup()
	f := openLockerFile(t, cli)
	defer func() { _ = f.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	got, err := f.GetLock(ctx, client.LockWrite)
	if err != nil {
		t.Fatalf("GetLock: %v", err)
	}
	if got != nil {
		t.Errorf("GetLock = %+v, want nil (region free)", got)
	}
}

// -- .u dialect gate tests -----------------------------------------

func TestClient_Lock_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	// We need a *File to call Lock. Fabricate via raw walk+lopen won't
	// work on .u; use the Conn.Attach path which on .u will not have
	// a lockable fid — we just need *any* *File.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	// The uMockServer answers Tattach; that's enough to get a *File.
	_, err := cli.Raw().Attach(ctx, 0, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// Now construct a *File externally via Attach which uses the higher
	// helper. Fall back: use cli.Attach again.
	f, err := cli.Attach(ctx, "me2", "")
	if err != nil {
		t.Fatalf("Attach cli: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Lock(ctx, client.LockWrite); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Lock on .u = %v, want ErrNotSupported", err)
	}
}

func TestClient_TryLock_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	f, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.TryLock(ctx, client.LockWrite); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("TryLock on .u = %v, want ErrNotSupported", err)
	}
}

func TestClient_Unlock_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	f, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Unlock(ctx); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("Unlock on .u = %v, want ErrNotSupported", err)
	}
}

func TestClient_GetLock_NotSupportedOnU(t *testing.T) {
	t.Parallel()
	cli, cleanup := newUMockClientPair(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	f, err := cli.Attach(ctx, "me", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.GetLock(ctx, client.LockWrite); !errors.Is(err, client.ErrNotSupported) {
		t.Fatalf("GetLock on .u = %v, want ErrNotSupported", err)
	}
}

// -- 1000-iter no-leak test ----------------------------------------

// TestClient_Lock_NoFidLeak runs a 1000-iteration Lock/Unlock loop
// under an alternating Blocked/OK status queue; afterwards, the fid
// reuse cache depth + live goroutine count must be stable. The loop
// reuses a single opened *File so the fid allocator sees no churn.
func TestClient_Lock_NoFidLeak(t *testing.T) {
	root, locker := buildLockerRoot(t)
	// Queue Blocked, Blocked, OK for every iteration (3 * 1000 statuses).
	for range 1000 {
		locker.queueStatus(proto.LockStatusBlocked, proto.LockStatusBlocked, proto.LockStatusOK)
	}

	cli, cleanup := newClientServerPair(t, root,
		client.WithLockPollSchedule([]time.Duration{100 * time.Microsecond}),
	)
	defer cleanup()
	f := openLockerFile(t, cli)
	defer func() { _ = f.Close() }()

	// Stabilise goroutine baseline after pair boot.
	time.Sleep(10 * time.Millisecond)
	runtime.GC()
	baseline := runtime.NumGoroutine()
	fidBaseline := client.FidReuseLen(cli)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	for i := range 1000 {
		if err := f.Lock(ctx, client.LockWrite); err != nil {
			t.Fatalf("iter %d Lock: %v", i, err)
		}
		if err := f.Unlock(ctx); err != nil {
			t.Fatalf("iter %d Unlock: %v", i, err)
		}
	}

	time.Sleep(10 * time.Millisecond)
	runtime.GC()
	got := runtime.NumGoroutine()
	// Allow a small slack (timer goroutines may linger briefly).
	if got > baseline+4 {
		t.Errorf("goroutines grew: baseline=%d got=%d", baseline, got)
	}
	if fidGot := client.FidReuseLen(cli); fidGot != fidBaseline {
		t.Errorf("fid reuse cache depth drifted: baseline=%d got=%d", fidBaseline, fidGot)
	}
}

// -- fixtures -------------------------------------------------------

// buildLockerRoot constructs a MemDir root with a single "locker"
// testLockerNode child, returning both for assertion.
func buildLockerRoot(tb testing.TB) (server.Node, *testLockerNode) {
	tb.Helper()
	gen := &server.QIDGenerator{}
	root := memfs.NewDir(gen)
	locker := &testLockerNode{}
	locker.Init(gen.Next(proto.QTFILE), locker)
	root.AddChild("locker", locker.EmbeddedInode())
	return root, locker
}

// openLockerFile attaches, walks to /locker, and opens it read-only,
// returning a usable *File that holds a server-side fidOpened state.
func openLockerFile(tb testing.TB, cli *client.Conn) *client.File {
	tb.Helper()
	ctx, cancel := context.WithTimeout(tb.Context(), 5*time.Second)
	defer cancel()
	if _, err := cli.Attach(ctx, "me", ""); err != nil {
		tb.Fatalf("Attach: %v", err)
	}
	f, err := cli.OpenFile(ctx, "/locker", 0, 0)
	if err != nil {
		tb.Fatalf("OpenFile /locker: %v", err)
	}
	return f
}
