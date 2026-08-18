# Releasing keydris-cli

The CLI ships as **prebuilt static binaries** on S3, served over CloudFront, and
installed with a `curl … | bash` one-liner. No Go or checkout required by users.

The same workflow also publishes the native CLI through the public `@keydris`
npm packages. See [npm-distribution.md](npm-distribution.md).

## Distribution at a glance

- **Base URLs — one host per channel:**
  - stable → `https://get.keydris.com/keydris-cli`
  - dev → `https://dev.get.keydris.com/keydris-cli`
- Both are alternate domain names on the same CloudFront distribution
  (`E24CJKXKFGBX5Z`) in front of the same S3 bucket (`keydris-cli-artifacts-dev`).
- **Channels:** `stable` and `dev` — still path prefixes in that one bucket. The
  hostname selects the prefix; see [Host-bound channels](#host-bound-channels).
- **Platforms:** `darwin`/`linux` × `amd64`/`arm64`, built `CGO_ENABLED=0`
  (static, stdlib-only).

### Object layout (S3 key → URL)

All artifacts live under the `keydris-cli/` bucket prefix, served at the same
path on CloudFront:

```text
keydris-cli/stable/install.sh                          https://get.keydris.com/keydris-cli/install.sh
keydris-cli/dev/install.sh                             https://dev.get.keydris.com/keydris-cli/install.sh
keydris-cli/install.sh                                 (legacy; see Rollout order — unreachable once the router is live)
keydris-cli/<channel>/<version>/keydris-<os>-<arch>     …/keydris-cli/<channel>/<version>/keydris-<os>-<arch>
keydris-cli/<channel>/<version>/SHA256SUMS              …/keydris-cli/<channel>/<version>/SHA256SUMS
keydris-cli/<channel>/latest/…                          …/keydris-cli/<channel>/latest/…   (mutable pointer)
keydris-cli/dev/<version>/keydris.toml                  …/keydris-cli/dev/<version>/keydris.toml
keydris-cli/dev/latest/keydris.toml                     …/keydris-cli/dev/latest/keydris.toml
keydris-cli/stable/<version>/keydris.toml               …/keydris-cli/stable/<version>/keydris.toml
keydris-cli/stable/latest/keydris.toml                  …/keydris-cli/stable/latest/keydris.toml
```

Note the asymmetry: the `<channel>` segment is present in artifact URLs but not
in the install URL, because CloudFront rewrites `/keydris-cli/install.sh` to the
requesting host's copy.

`latest/` and the `install.sh` objects are published with
`Cache-Control: max-age=60`; versioned paths are immutable. Each publish
invalidates `/keydris-cli/install.sh`, both per-channel `install.sh` objects, and
`/keydris-cli/<channel>/latest/*` on CloudFront.

## Host-bound channels

`install.sh` is one script served from two hostnames, and a piped script cannot
see the URL it was fetched from. So the channel and its download host are baked
in at publish time by [scripts/render-install.sh](../scripts/render-install.sh),
which emits one flavor per channel, and a CloudFront **viewer-request function**
(`keydris-cli-install-router`, deployed from the infra repo) routes each host to
its own copy:

| Host | `/keydris-cli/install.sh` resolves to | Everything else |
| --- | --- | --- |
| `get.keydris.com` | `keydris-cli/stable/install.sh` | `/keydris-cli/stable/*` only; other paths → 403 |
| `dev.get.keydris.com` | `keydris-cli/dev/install.sh` | `/keydris-cli/dev/*` only; other paths → 403 |

The cross-channel 403 is what makes the binding real: a stable host can never
hand out a dev binary, whatever env vars the caller sets. The rendered installer
enforces the same rule earlier and with a better message — it exits 1 if
`KEYDRIS_CHANNEL` names the other channel, unless `KEYDRIS_BASE_URL` is also set
(the escape hatch for mirrors and local testing).

Viewer-request functions run before the cache lookup and the rewritten URI
becomes the cache key, so both hosts share one distribution without Host in the
cache key and without cross-host cache bleed.

`keydris upgrade` mirrors the same map in `channelBaseURL`
([internal/cli/upgrade.go](../internal/cli/upgrade.go)) — the channel picks the
host, so an upgrade never crosses channels either.

### Changing the hostnames

Three places must agree, or an installed binary will point at a host that no
longer serves it: `channelBaseURL` in `internal/cli/upgrade.go`, the `base` case
statement in `scripts/render-install.sh`, and `CHANNEL_BY_HOST` in the CloudFront
function. The first two are pinned to each other by
`internal/cli/channel_binding_test.go`; the third lives in the infra repo and
nothing here can check it.

### Infrastructure prerequisites

Deployed from Terraform, not this repo:

- An ACM certificate in `us-east-1` covering **both** names. A
  `*.get.keydris.com` wildcard does *not* cover `get.keydris.com` itself, so the
  cert needs both as explicit SANs.
- `get.keydris.com` and `dev.get.keydris.com` as alternate domain names on
  `E24CJKXKFGBX5Z`, plus their Route 53 alias records.
- The `keydris-cli-install-router` CloudFront Function attached to the default
  cache behavior as a **viewer-request** function.

### Rollout order

`https://dev.get.keydris.com/keydris-cli/install.sh` is live today and must keep
working through the transition, so the two halves land in this order:

1. **Publish first.** A release creates `keydris-cli/{stable,dev}/install.sh` and
   writes the **dev** flavor to the legacy `keydris-cli/install.sh`. Until the
   router exists, that legacy key is what the dev URL serves, and the dev flavor
   points at `dev.get.keydris.com`, which resolves. (Writing the stable flavor
   there instead would aim the only live URL at `get.keydris.com` before DNS
   exists — that is the one ordering that breaks users.) The visible change at
   this step is that the dev URL starts defaulting to the dev channel instead of
   stable, which is the intended end state arriving early.
2. **Then deploy** the cert, both aliases, Route 53, and the router function.
   `get.keydris.com` goes live and each host locks to its channel.
3. **Optionally clean up.** Once the router is confirmed, `keydris-cli/install.sh`
   is unreachable through CloudFront (the function rewrites every request for it)
   and can be deleted from the bucket and from the publish steps.

Doing step 2 first would 404: the router rewrites to per-channel objects that do
not exist until a release publishes them.

## Automated releases (CI)

[.github/workflows/release.yml](../.github/workflows/release.yml) publishes on:

| Trigger | S3 channel/version | npm version/dist-tag |
| --- | --- | --- |
| push tag `v*` | `stable`, the tag (e.g. `v0.1.0`) | tag without `v`, `latest` |
| push to `main` | `dev`, `git describe` | `<base>-dev.<run-id>.<attempt>`, `next` |

It builds and verifies both release formats, syncs to S3, invalidates
CloudFront, publishes all six native npm packages, verifies their registry
visibility, and publishes `@keydris/cli` last. AWS and npm both authenticate via
GitHub OIDC; no static publishing keys are stored.

**Required repo settings** (Settings → Secrets and variables → Actions):

| Kind | Name | Value |
| --- | --- | --- |
| Secret | `AWS_RELEASE_ROLE_ARN` | `arn:aws:iam::412268502805:role/keydris-dev-gha-cli-publish` |
| Variable | `KEYDRIS_RELEASE_BUCKET` | `keydris-cli-artifacts-dev` |
| Variable | `KEYDRIS_CLOUDFRONT_DISTRIBUTION_ID` | `E24CJKXKFGBX5Z` |
| Variable | `AWS_REGION` | the bucket's region (e.g. `us-east-1`) |

On npmjs.com, configure `keydrisLabs/keydris-cli` and workflow filename
`release.yml` as a trusted publisher for each of the seven `@keydris` packages.
Enable the `npm publish` action. The repository is private, so CI disables npm
provenance while retaining OIDC authentication.

To cut a stable release:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

Pushing to `main` refreshes the `dev` channel automatically.

## Manual release (from a workstation with AWS creds)

```bash
make dist                                # cross-compile + SHA256SUMS into dist/
make release \
  CHANNEL=dev \
  S3_BUCKET=keydris-cli-artifacts-dev \
  DISTRIBUTION_ID=E24CJKXKFGBX5Z         # optional: invalidates CloudFront
```

`VERSION` defaults to `git describe --tags --always --dirty`; override with
`make release VERSION=v0.1.0 …`.

## How users install

```bash
# stable
curl -fsSL https://get.keydris.com/keydris-cli/install.sh | bash

# dev
curl -fsSL https://dev.get.keydris.com/keydris-cli/install.sh | bash
```

No channel env var: the URL picks the channel. Both drop a zero-config
`~/.keydris.toml` for that channel's control plane + IdP, including a
`channel = "…"` line that keeps later `keydris upgrade` runs on the same channel.

Installer env: `PREFIX`, `KEYDRIS_VERSION` (default `latest`),
`KEYDRIS_BASE_URL` (mirror/local testing), `KEYDRIS_NO_CONFIG=1`. It verifies the
download against `SHA256SUMS` before installing. `keydris version` reports the
installed build.

## Versioning

The version is stamped at link time:
`-ldflags "-X github.com/keydrisLabs/keydris-cli/internal/cli.Version=<v>"`
(handled by `make build` / `make dist`). Unstamped builds report `dev`.

## Notes / gotchas

- **`stable` currently lands in the `-dev` bucket.** There's only a dev artifacts
  environment so far, so tagged stable releases publish to
  `keydris-cli-artifacts-dev` under `/stable/` — and now `get.keydris.com` serves
  out of that dev-named bucket too, which makes the wart more visible without
  changing what it is. When a prod artifacts bucket exists, set
  `KEYDRIS_RELEASE_BUCKET` per environment (or split the workflow) so stable
  isn't served from a dev bucket. Splitting the buckets would also let each host
  have its own distribution, at which point the function's cross-channel 403s
  become redundant (the rewrite of `/keydris-cli/install.sh` does not).
- **Installers publish on every run, both flavors.** A push to `main` republishes
  the *stable* `install.sh` too, since the script is bound to a channel by its
  host, not by the channel being released. An installer change therefore reaches
  stable users as soon as it lands on `main`, before any tag — the same exposure
  the single `install.sh` object had before, now spread across two objects.
- **Path prefix.** Artifacts live under `keydris-cli/` in the bucket, matching the
  publish role's `s3:prefix` scope and the CloudFront path — so the install URL is
  `…/keydris-cli/install.sh`. To switch to a clean root layout (`…/install.sh`),
  widen the IAM `ListBucket`/`PutObject` scope in Terraform and drop the prefix
  from `install.sh` + the workflow + `Makefile` together.
- **macOS Gatekeeper.** `curl`-downloaded binaries aren't quarantined and run
  fine; a *browser* download would be blocked as "unidentified developer." The
  fix is Apple Developer ID signing + notarization — deferred until past dev.
