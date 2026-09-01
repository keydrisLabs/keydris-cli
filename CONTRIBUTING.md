# Contributing to keydris-cli

Thanks for helping out. This repository is the agent-side half of Keydris: one Go binary that is simultaneously a
short-lived hook process and a long-running enforcement proxy. The thing that makes it unusual is that **it is an
enforcement point, so it has no fail-open branch** — most of the guidance below follows from that.

Questions and design discussion are welcome in [Discord](https://discord.gg/3JUcXkUTu) or an
[issue](https://github.com/keydrisLabs/keydris-cli/issues). Vulnerabilities go through [SECURITY.md](SECURITY.md)
instead, never a public issue.

---

## The rules specific to this repository

### 1. Every error denies

An error in the enforcement path or in a gating hook must produce a rejection. Not a warning, not a pass-through, not
a `continue`. If you find yourself adding a branch where an unexpected condition results in the request proceeding,
that is the thing to reconsider.

This is not defensive style, it is a correctness requirement: both Claude Code and Codex fail **open** when a hook
crashes, times out, or prints invalid JSON. That is why [`pretool.go`](internal/cli/pretool.go) always exits 0 and
always prints a verdict — the deny travels in the payload, never in the exit code, and even the
verdict-encoding failure path has a hard-coded literal.

**A test that only asserts the allow path is not a test of this code.** A change to enforcement needs a test on the
deny path in the same pull request.

### 2. Decode strictly, at the boundary

Everything arriving from the control plane, the harness, or the agent is decoded with unknown fields rejected,
duplicate JSON keys refused, trailing values refused, and every identity pattern checked. `decodeStrict` and
`rejectDuplicateJSONKeys` in [`internal/runtimecontract/`](internal/runtimecontract/) exist to be used, not to be
worked around.

Duplicate keys in particular are refused because parsers disagree about which value wins — a decision made on one
reading could be executed on another.

### 3. Trust the transport, not the payload

Scheme, host, and port come from the connection the data plane actually made (`Flow.Scheme()`, `Flow.DstHost()`,
`Flow.DstPort()`). A routing value is resolved from the request path or body — `owner/repo` from a GitHub path,
`channel` from a Slack body — never from a header or anything else the agent sets freely.

### 4. Local checks may only narrow

Every decision the CLI makes is re-enforced server-side at execution time. A local check exists to answer cheaply and
to fail closed. It may **never** be the sole reason something is allowed.

### 5. Never widen a secret's reach

No new logging of a KIT, a per-session proxy token, or an injected credential value. New audit fields go through
`sanitizeAuthorizeText` in [`internal/node/daemon/audit.go`](internal/node/daemon/audit.go). New files under the data
directory are `0600` inside a `0700` directory. The session socket logs a handle **prefix**, never a whole handle.

If you are only fixing a typo in a comment or a document, none of this applies.

---

## Repository layout

| Path | What it is |
| --- | --- |
| [`cmd/keydris`](cmd/keydris/) | `main()`; everything else is `internal` |
| [`internal/cli`](internal/cli/) | Every user-facing command, plus the four internal hook entrypoints |
| [`internal/runtimecontract`](internal/runtimecontract/) | The control-plane wire contract and its strict decoders |
| [`internal/node/daemon`](internal/node/daemon/) | The long-running service: flow loop, route enforcement, renewal, audit |
| [`internal/node/dataplane`](internal/node/dataplane/) | Interception, behind one interface |
| [`internal/node/sandbox`](internal/node/sandbox/) | What `init` writes into `~/.claude/settings.json` and `~/.codex/hooks.json` |
| [`internal/config`](internal/config/) | Layered configuration and the managed-scope file |
| [`internal/proxyscope`](internal/proxyscope/) | Origin canonicalization and matching |
| [`internal/evidence`](internal/evidence/) | The hash-chained ledger behind `keydris logs` |
| [`npm/`](npm/) | The `@keydris/cli` launcher and six native packages — packaging only, no reimplementation |

The split between `internal/runtimecontract` and everything else is load-bearing. Wire shapes, validation, and the
decision enum live there and nowhere else, so there is exactly one place where "what the control plane may say" is
defined.

---

## Development setup

Go 1.22 or newer. There is one Go dependency (a JSON canonicalizer); everything else is the standard library.

```bash
git clone https://github.com/keydrisLabs/keydris-cli.git
cd keydris-cli

make build        # version-stamped binary into bin/
make vet          # go vet ./...
make test         # go test ./...
make install      # build + install to /usr/local/bin  (PREFIX=$HOME/.local make install)
make clean        # remove bin/, dist/, and the in-repo build cache
```

`GOCACHE` is set to `.gobuild` inside the repository so builds work in restricted sandboxes.

On Windows, build natively:

```powershell
go build -o bin\keydris.exe .\cmd\keydris
```

### Running against a control plane

The CLI needs a reachable control plane for anything past `keydris version`. Point it at yours with
`KEYDRIS_CONTROL_URL` and `KEYDRIS_CONTROL_MTLS_URL` (see [.env.example](.env.example) and
[.keydris.toml.example](.keydris.toml.example)), or install a channel config, which sets both.

For local iteration on the enforcement path, the router and the wire decoders are exercised entirely by
`internal/node/daemon/runtime_router_test.go` and `internal/runtimecontract/*_test.go` against in-process stubs — no
control plane required.

### The npm workspace

```bash
cd npm
npm run build:native   # static binaries for six platform targets
npm test               # manifests, executable signatures, launcher, pack file lists, offline install
npm run clean          # before committing
```

Node 20 or newer. Every script uses only Node's standard library; generated executables are gitignored.

### The eBPF plane (Linux only)

Behind the `ebpf` build tag, and artifacts are generated rather than committed:

```bash
make ebpf-vmlinux   # needs bpftool
make ebpf-gen       # needs clang
make ebpf-build
make ebpf-spike     # sudo; a real end-to-end attribution check
```

---

## Checks to run

Run the relevant checks locally before opening a pull request:

| Check | Command |
| --- | --- |
| Vet | `make vet` |
| Tests | `make test` |
| Cross-compile the release matrix | `make dist` |
| npm packaging, if you touched `npm/` | `cd npm && npm run build:native && npm test` |

`make dist` is worth running for any change under `internal/node/` — the release matrix is
`darwin`/`linux` × `amd64`/`arm64` with `CGO_ENABLED=0`, and a build-tag or syscall mistake shows up there rather
than in `go test` on your own platform.

---

## Conventions

- **Standard library first.** One Go dependency, deliberately. New functionality that needs a third-party module
  should be discussed in an issue before the pull request.
- **Platform code goes behind build tags,** matching the existing `_linux.go` / `_windows.go` / `_unix.go` /
  `_other.go` split. Every `_other.go` must be a working no-op, not a panic.
- **Package comments carry the "why".** Each package's doc comment explains what it owns and where its trust boundary
  is. Keep them accurate — several are the only place a design decision is written down.
- **Comments explain decisions, not mechanics.** The existing code comments the non-obvious branch (why a
  present-but-unknown token is unattributed; why the Codex hooks are split in two). Match that density.
- **Errors are messages an agent reads.** A rejection reason is surfaced to the model. Write a sentence it can act on,
  and never interpolate a token, a credential, or a raw upstream body into one.
- **`gofmt` is the formatter.** Match the surrounding style otherwise.
- **Tests live next to what they test,** as `*_test.go` in the same package, and use in-process stubs rather than a
  live control plane.

### Things that must stay in sync

| If you change | Also update |
| --- | --- |
| The command set | `usage()` in [`cli.go`](internal/cli/cli.go) **and** the Commands block in [README.md](README.md) |
| `channelBaseURL` in [`upgrade.go`](internal/cli/upgrade.go) | `scripts/render-install.sh` — [`channel_binding_test.go`](internal/cli/channel_binding_test.go) pins them to each other |
| A wire shape in `internal/runtimecontract` | The corresponding protocol example in [README.md](README.md) |
| What `init` writes | [`examples/claude-code/settings.json`](examples/claude-code/settings.json) or [`examples/codex/`](examples/codex/) |
| An npm manifest | All seven, via `npm run version:packages` — never by hand |

---

## Pull requests

1. Fork and branch: `git checkout -b feat/your-change`.
2. Make the change. If it touches enforcement, add the deny-path test in the same pull request.
3. Run `make vet && make test`, plus `make dist` for anything under `internal/node/`.
4. Update whatever the sync table above says you owe.
5. Write a commit message that says what changed and why.
6. Open the pull request against `main`, and describe the behavior change, not just the diff. For an enforcement
   change, say explicitly what now gets denied that did not before, or the reverse.

Small, reviewable pull requests move faster than large ones. If you are planning something structural — a new data
plane, a new provider executor, a change to the runtime contract — open an issue or ask in Discord first, so we can
agree on the shape before you build it.

---

## Licensing of contributions

This project is licensed under the [Apache License 2.0](LICENSE). By submitting a contribution you agree that it is
licensed under those same terms, including the patent grant in section 5, and that you have the right to submit it.
New source files should carry no separate license header; the repository-level `LICENSE` and [`NOTICE`](NOTICE) cover
them.

---

## Code of conduct

Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md). It applies to issues, pull requests, and the
Discord.
