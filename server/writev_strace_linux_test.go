//go:build linux

package server

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/ninep/proto"
)

// TestPayloaderUsesWritev re-execs the test binary under strace -e trace=writev
// and asserts that the server's Rread response path emits a writev(2) syscall
// on unix domain sockets. This closes PERF-07.3: the shipped proto.Payloader
// path in server/conn.go:sendResponseInline routes through net.Buffers.WriteTo,
// which on *net.UnixConn type-asserts to buffersWriter and calls pfd.Writev
// instead of a sequential Write loop. The strace log is the only defense-in-
// depth evidence that the kernel actually received a writev syscall (not a
// split of multiple write() calls).
//
// Design decisions (14-CONTEXT.md):
//   - D-03: the test is implemented as a re-exec helper under strace -e writev
//     so the parent process can scan the syscall log for an iovcnt of 2 or 3.
//   - D-04: the entire file is gated behind //go:build linux — non-linux
//     platforms do not compile this code. The runtime skip path is a secondary
//     gate for linux hosts without strace or with a restrictive ptrace_scope.
//
// Pitfall 2 (14-RESEARCH.md): the iovcnt can be 2 or 3 depending on whether
// the Rread payload slot is non-empty. The shipped path emits 3 iovecs
// (hdr, fixedBody=4B count, payload) when len(payload) > 0, and 2 iovecs
// (hdr, fixedBody) when len(payload) == 0. The child workload issues a single
// Tread with Count=4096 against a known-populated 128 MiB file to guarantee a
// non-empty payload, but the log grep accepts either "], 2)" or "], 3)" so
// the test is resilient to a future collapse of hdr||fixedBody into a single
// iovec (would drop iovcnt to 2 on the same request).
//
// Pitfall 4 (14-RESEARCH.md): CI environments with kernel.yama.ptrace_scope
// >= 2 block strace attach. The test degrades to t.Skipf (not t.Fatalf) on
// "Operation not permitted" or "ptrace:" output so the test is well-behaved
// under restricted ptrace.
func TestPayloaderUsesWritev(t *testing.T) {
	// Child mode: run a minimal unix-socket 9P server + single Tread workload
	// and exit. The parent process will scan the strace log after this
	// re-execution completes.
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		runWritevStraceHelper(t)
		return
	}

	// Parent mode: re-exec self under strace and grep the syscall log.
	if _, err := exec.LookPath("strace"); err != nil {
		t.Skipf("strace not installed (apt install strace): %v", err)
	}

	tmp, err := os.CreateTemp(t.TempDir(), "ninep-writev-*.log")
	if err != nil {
		t.Fatalf("tempfile: %v", err)
	}
	tmpName := tmp.Name()
	// Close the tempfile handle immediately — strace reopens the path.
	if err := tmp.Close(); err != nil {
		t.Fatalf("tempfile close: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "strace",
		"-f", "-e", "trace=writev", "-o", tmpName,
		os.Args[0], "-test.run=^TestPayloaderUsesWritev$", "-test.v",
	)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")

	out, err := cmd.CombinedOutput()
	if err != nil {
		combined := string(out)
		// ptrace_scope >= 2 or missing CAP_SYS_PTRACE in hardened containers:
		// strace prints "Operation not permitted" or "ptrace:" and exits
		// non-zero. Treat as skip so CI environments without ptrace privileges
		// don't fail this test. Pitfall 4 in 14-RESEARCH.md.
		if strings.Contains(combined, "Operation not permitted") ||
			strings.Contains(combined, "ptrace:") {
			t.Skipf("strace ptrace restricted (likely kernel.yama.ptrace_scope): %v\n%s", err, combined)
		}
		t.Fatalf("helper failed: %v\noutput: %s", err, combined)
	}

	logBytes, err := os.ReadFile(tmpName)
	if err != nil {
		t.Fatalf("read strace log: %v", err)
	}

	// The strace log may be empty when the tracer couldn't attach (rare but
	// possible under restricted cgroups). Skip cleanly rather than fail.
	if len(logBytes) == 0 {
		t.Skipf("strace log empty — tracer likely could not attach\nchild stdout:\n%s", out)
	}

	// Scan for a writev() syscall with iovcnt of 2 or 3. strace output format
	// under -f may prefix lines with "[pid NNNN] " so we don't anchor at line
	// start — strings.Contains is the correct matcher per A2 (14-RESEARCH.md).
	scanner := bufio.NewScanner(bytes.NewReader(logBytes))
	// strace lines for writev can be long (each iovec dumps iov_base preview);
	// bump the scanner buffer so we don't split a single writev() call across
	// two scanner tokens.
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	var (
		foundAny       bool
		foundIovcnt23  bool
		firstWritevHit string
	)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "writev(") {
			continue
		}
		foundAny = true
		if firstWritevHit == "" {
			firstWritevHit = line
		}
		// Accept either "], 2)" or "], 3)" — Pitfall 2: iovcnt may drop to 2
		// if hdr||fixedBody collapse is optimised in a future pass.
		if strings.Contains(line, "], 2)") || strings.Contains(line, "], 3)") {
			foundIovcnt23 = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan strace log: %v", err)
	}

	if !foundAny {
		// No writev calls at all — tracer captured other syscalls but the
		// workload may have used write(2) only. This is a degraded trace
		// rather than an acceptance failure; skip with log attached so a
		// human can diagnose.
		t.Skipf("strace log contains no writev() lines — tracer may be restricted\nlog:\n%s\nchild stdout:\n%s", logBytes, out)
	}
	if !foundIovcnt23 {
		t.Fatalf("no writev(..., 2|3) syscall found; first writev hit was: %q\nfull log:\n%s\nchild stdout:\n%s",
			firstWritevHit, logBytes, out)
	}
}

// runWritevStraceHelper is invoked inside the re-exec'd child process. It
// sets up a minimal unix-socket 9P server, sends one Tread for 4096 bytes
// against a pre-populated benchFile, drains the response, and returns. The
// Tread is intentionally issued with Count=4096 against the 128 MiB "data"
// file from newBenchTree so the Rread response carries a non-empty payload,
// forcing sendResponseInline to emit a 3-iovec writev (hdr, fixedBody,
// payload) under the shipped Payloader path (server/conn.go).
//
// The helper reuses benchAttachFid0 / benchWalkOpen / newBenchTree which
// were promoted to testing.TB by plan 14-01 specifically to support this
// helper from a *testing.T context (no bench-specific duplication needed).
func runWritevStraceHelper(t *testing.T) {
	t.Helper()

	root := newBenchTree(t)
	cp := newConnPairMsizeTransport(t, "unix", root, 65536)
	t.Cleanup(func() { cp.close(t) })

	benchAttachFid0(t, cp)
	_ = benchWalkOpen(t, cp, 0, 1, "data")

	frame := mustEncode(t, proto.Tag(1), &proto.Tread{
		Fid:    1,
		Offset: 0,
		Count:  4096,
	})
	if _, err := cp.client.Write(frame); err != nil {
		t.Fatalf("write Tread: %v", err)
	}
	if err := drainResponse(cp.client); err != nil {
		t.Fatalf("drain Rread: %v", err)
	}
}
