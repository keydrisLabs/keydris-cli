# Contributing to Keydris CLI

Thank you for your interest in improving Keydris! This document covers
everything you need to get from a fresh clone to a merged pull request.

By participating in this project you agree to abide by our
[Code of Conduct](CODE_OF_CONDUCT.md).

> **Reporting a security vulnerability?** Please follow the
> [security policy](SECURITY.md) instead of opening a public issue.

## Getting started

### Prerequisites

| Tool | Version | Needed for |
| --- | --- | --- |
| [Go](https://go.dev/dl/) | ≥ 1.22 | Everything |
| `make` | any recent | Build/test shortcuts |
| [Node.js](https://nodejs.org/) | ≥ 20 | Only the npm packaging checks under `npm/` |
| `clang` + `bpftool` | recent | Only the optional Linux eBPF path (`-tags ebpf`) |

### Build and run

```bash
git clone https://github.com/keydrisLabs/keydris-cli.git
cd keydris-cli
make build              # → ./bin/keydris
./bin/keydris help
```

On Windows (PowerShell):

```powershell
go build -o bin\keydris.exe .\cmd\keydris
```

### Run the tests

```bash
make test                              # go test ./...
make vet                               # go vet ./...
go test -race -cover ./...             # what CI runs
```

The npm distribution has its own verification suite (manifest integrity,
binary signatures, launcher behavior, offline install). It verifies real
binaries, so build them first:

```bash
cd npm
npm run build:native    # cross-compiles the Go binaries the packages wrap
npm test
```

All of these must pass before a pull request can merge — CI runs them on every
push and pull request.

## Project layout

```text
cmd/keydris/            entry point — delegates to internal/cli
internal/cli/           command dispatch (hand-rolled switch, stdlib flag)
internal/node/          proxy data planes, daemon, session attribution
internal/evidence/      hash-chained audit ledger
internal/config/        env + TOML configuration and its trust boundaries
internal/runtimecontract/  control-plane contract schemas + fixtures
docs/                   design docs (sandbox, attribution, codex, npm)
npm/                    npm distribution packages and checks
deploy/                 systemd unit and release-channel configs
```

## Development guidelines

### Keep the dependency footprint minimal

The binary is deliberately **stdlib-only plus a single third-party
dependency** (JSON canonicalization). This is a security tool that ends up on
developer machines with a local CA — every dependency is attack surface.
Adding a new module requires strong justification in the PR description;
prefer the standard library.

### Fail closed

Enforcement code paths must produce an explicit deny on *every* error — an
unreachable control plane, a timeout, a malformed payload, a missing session.
If your change touches authorization, hooks, or the proxy, include tests for
the failure paths, not just the happy path.

### Code style

- `gofmt` formatting (CI runs `go vet`; unformatted code will be flagged in review).
- Match the surrounding code's idiom: table-driven tests, stdlib `flag`
  flagsets per subcommand, small focused files.
- Comments explain *constraints and invariants*, not what the next line does.

### Cross-platform

The CLI ships for macOS, Linux, and Windows (via npm). Avoid Unix-only
assumptions in shared code paths — path handling, process signalling, and
file permissions all differ. Platform-specific behavior belongs behind
build tags or explicit `runtime.GOOS` checks with a comment.

## Submitting changes

1. **Fork and branch.** Branch from `main`; use a descriptive name
   (`feat/scope-detection`, `fix/proxy-pidfile`).
2. **Write tests.** New behavior needs tests; bug fixes need a regression test
   that fails without the fix.
3. **Use conventional commits.** We follow the
   [Conventional Commits](https://www.conventionalcommits.org/) style:

   ```
   feat(scope): detect proxy scope from the agent's policy
   fix(install): overwrite existing ~/.keydris.toml with the channel config
   docs(readme): clarify the Codex quickstart
   ```

4. **Keep PRs focused.** One logical change per pull request. Include a clear
   description of *what* changed and *why*; link related issues.
5. **Make CI green.** `go vet`, the test suite, and the npm checks all run
   automatically.

### First-time contributors

Look for issues labeled `good first issue`. If you want to work on something
larger, open an issue first to discuss the approach — it saves everyone time.

## Releases

Releases are cut automatically by CI: a `v*` tag publishes the **stable**
channel and a push to `main` publishes the **dev** channel. Contributors don't
need to do anything release-related; maintainers handle tagging.

## License

By contributing, you agree that your contributions will be licensed under the
[Apache License 2.0](LICENSE), the same license that covers the project
(inbound = outbound).
