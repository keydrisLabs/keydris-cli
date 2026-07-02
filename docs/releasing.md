# Releasing keydris-cli

The CLI ships as **prebuilt static binaries** on S3, served over CloudFront, and
installed with a `curl … | bash` one-liner. No Go or checkout required by users.

## Distribution at a glance

- **Base URL:** `https://dev.get.keydris.com` (CloudFront → S3 bucket
  `keydris-cli-artifacts-dev`, distribution `E24CJKXKFGBX5Z`).
- **Channels:** `stable` and `dev` — they are path prefixes in the same bucket.
- **Platforms:** `darwin`/`linux` × `amd64`/`arm64`, built `CGO_ENABLED=0`
  (static, stdlib-only).

### Object layout (S3 key → URL)

```
install.sh                                  https://dev.get.keydris.com/install.sh
<channel>/<version>/keydris-<os>-<arch>      …/<channel>/<version>/keydris-<os>-<arch>
<channel>/<version>/SHA256SUMS               …/<channel>/<version>/SHA256SUMS
<channel>/latest/…                           …/<channel>/latest/…        (mutable pointer)
dev/<version>/keydris.toml                   …/dev/<version>/keydris.toml (dev channel only)
dev/latest/keydris.toml                      …/dev/latest/keydris.toml
```

`latest/` and `install.sh` are published with `Cache-Control: max-age=60`;
versioned paths are immutable. Each publish invalidates `/install.sh` and
`/<channel>/latest/*` on CloudFront.

## Automated releases (CI)

[.github/workflows/release.yml](../.github/workflows/release.yml) publishes on:

| Trigger | Channel | Version |
| --- | --- | --- |
| push tag `v*` | `stable` | the tag (e.g. `v0.1.0`) |
| push to `main` | `dev` | `git describe` (commit-derived) |

It builds the matrix, verifies checksums, then syncs to S3 and invalidates
CloudFront, authenticating to AWS via GitHub OIDC (no static keys).

**Required repo settings** (Settings → Secrets and variables → Actions):

| Kind | Name | Value |
| --- | --- | --- |
| Secret | `AWS_RELEASE_ROLE_ARN` | `arn:aws:iam::412268502805:role/keydris-dev-gha-cli-publish` |
| Variable | `KEYDRIS_RELEASE_BUCKET` | `keydris-cli-artifacts-dev` |
| Variable | `KEYDRIS_CLOUDFRONT_DISTRIBUTION_ID` | `E24CJKXKFGBX5Z` |
| Variable | `AWS_REGION` | the bucket's region (e.g. `us-east-1`) |

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
curl -fsSL https://dev.get.keydris.com/install.sh | bash

# dev — also drops a zero-config ~/.keydris.toml (dev control plane + IdP)
curl -fsSL https://dev.get.keydris.com/install.sh | KEYDRIS_CHANNEL=dev bash
```

Installer env: `PREFIX`, `KEYDRIS_CHANNEL` (`stable`|`dev`), `KEYDRIS_VERSION`
(default `latest`), `KEYDRIS_BASE_URL`. It verifies the download against
`SHA256SUMS` before installing. `keydris version` reports the installed build.

## Versioning

The version is stamped at link time:
`-ldflags "-X github.com/keydrisLabs/keydris-cli/internal/cli.Version=<v>"`
(handled by `make build` / `make dist`). Unstamped builds report `dev`.

## Notes / gotchas

- **`stable` currently lands in the `-dev` bucket.** There's only a dev artifacts
  environment so far, so tagged stable releases publish to
  `keydris-cli-artifacts-dev` under `/stable/`. When a prod artifacts bucket
  exists, set `KEYDRIS_RELEASE_BUCKET` per environment (or split the workflow) so
  stable isn't served from a dev bucket.
- **Root layout.** Artifacts live at the bucket/domain root (no `/keydris-cli/`
  prefix), matching the CloudFront base URL. If the origin path ever changes,
  update `KEYDRIS_BASE_URL` in `install.sh` and the S3 keys together.
- **macOS Gatekeeper.** `curl`-downloaded binaries aren't quarantined and run
  fine; a *browser* download would be blocked as "unidentified developer." The
  fix is Apple Developer ID signing + notarization — deferred until past dev.
