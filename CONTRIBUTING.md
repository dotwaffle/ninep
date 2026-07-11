# Contributing to ninep

Thanks for your interest in contributing. ninep is a Go library implementing
the 9P2000.L and 9P2000.u network filesystem protocols, with a
capability-based API inspired by [go-fuse/v2/fs](https://pkg.go.dev/github.com/hanwen/go-fuse/v2/fs).
This guide covers what to expect when contributing code, tests, or docs.

## Code of Conduct

Be respectful, constructive, and assume good faith. Harassment, personal
attacks, and discriminatory language are not welcome in issues, pull requests,
or any other project space. If you experience or witness unacceptable
behavior, please open an issue or contact the maintainer directly.

## Ways to Contribute

- **Bug reports** -- open a GitHub issue with a minimal reproduction (Go
  version, OS, kernel version if relevant, and a code snippet or test case).
  Protocol-level issues are especially valuable when reproduced with a trace
  of the offending 9P message sequence.
- **Feature requests** -- open an issue describing the use case and, if
  possible, the shape of the API you have in mind. Capability-interface
  additions should explain which 9P operation they cover and how the
  ENOSYS-default pattern applies.
- **Pull requests** -- fixes, new capability interfaces, middleware, tests,
  benchmarks, and documentation improvements are all welcome.
- **Documentation** -- corrections and clarifications to files under `docs/`
  or package godoc are always appreciated.

## Development Setup

See [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) for prerequisites, build
instructions, test commands, and coding conventions. In short: Go >= 1.26 and
`golangci-lint`; Linux is required for `server/passthrough` tests.

For a quick tour of the codebase layout and runtime architecture, see
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md). For configuration knobs
(functional options, message-size limits, OTel wiring), see
[`docs/CONFIGURATION.md`](docs/CONFIGURATION.md).

## Pull Request Process

1. Fork the repo and create a feature branch from `main`.
2. Make focused commits -- one logical change per commit where practical.
3. Add or update tests for any behavior change. New capability interfaces
   require bridge tests and ideally an `fstest` scenario.
4. Run the full local gate before pushing:
   ```bash
   go vet ./...
   go test -race -count=1 ./...
   go build -trimpath ./...
   golangci-lint run ./...
   ```
5. Open a pull request against `main`. Describe the problem, the approach,
   and any trade-offs. Link relevant issues.
6. Be responsive to review feedback. Expect discussion on API shape,
   allocation behavior on hot paths, and protocol-correctness edge cases.

## Commit Message Conventions

Commits follow the Linux kernel style: a `subsystem: summary` subject line
in imperative mood. Look at `git log` for the established pattern.

```
subsystem: short imperative summary

Optional body explaining what the patch solves and why. Wrap at about
74 columns. Plain text only -- no links.
```

The subsystem identifies the affected area without requiring the reader
to inspect the diff (e.g. `server`, `client`, `passthrough`, `proto/p9l`,
`memfs`, `bufpool`). Keep subjects around 50 columns when practical and
lower-case.

```
server: zero-copy Rread payload via Payloader interface
client: reject oversized peer counts
passthrough: pin opens to resolved fds
```

Prefer one logical change per patch; each commit should compile and pass
the relevant tests on its own so `git bisect` stays useful. Do **not**
use Conventional Commits prefixes, and do **not** add `Co-Authored-By`
trailers.

## Testing Expectations

- All new code must include tests. Use table-driven tests (see existing
  `*_test.go` files for the pattern) and `t.Parallel()` where safe.
- Protocol-level tests use the `net.Pipe()` + `newConnPair` helper in
  `server/walk_test.go`. Filesystem-implementation tests use the harness in
  `server/fstest/`.
- Run with the race detector locally: `go test -race -count=1 ./...`.
- Codec changes in `proto/p9l` or `proto/p9u` should pass the existing fuzz
  targets. CI runs 60-second fuzz passes across four targets: the p9l and
  p9u codecs, plus `FuzzClientLock` and `FuzzClientXattr` in `client/`.
- Performance-sensitive changes should include a benchmark and, where
  possible, `benchstat` output in the PR description.

CI (`.github/workflows/ci.yml`) runs `go vet`, `go test -race -count=1`,
`go build -trimpath`, `golangci-lint`, a compile check of integration-tagged
tests, and a 60-second fuzz pass across four targets (p9l codec, p9u codec,
`FuzzClientLock`, `FuzzClientXattr`). All jobs must be green before merge.

## Code Review Expectations

- Reviews focus on API shape, protocol correctness, allocation behavior on
  hot paths, and test coverage.
- Small, focused PRs are easier to review and more likely to land quickly.
  Split large changes into a series.
- Exported items need godoc comments (`// Foo does ...`). Keep the exported
  surface minimal -- prefer unexported helpers.
- Accept interfaces, return concrete types, unless abstraction is required.
- Respond to review comments inline. If you disagree, explain your
  reasoning; the goal is the right design, not winning an argument.

## License

ninep is released under the BSD 3-Clause License (see [`LICENSE`](LICENSE)).
By submitting a pull request, you agree that your contribution will be
licensed under the same terms. You retain copyright on your contributions;
the BSD 3-Clause license grants the project and its users the rights needed
to use, modify, and redistribute the code.
