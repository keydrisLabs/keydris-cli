# Releasing keydris-cli

The CLI ships as **prebuilt static binaries** on S3, served over CloudFront, and
installed with a `curl … | bash` one-liner. No Go or checkout required by users.

The same workflow also publishes the native CLI through the public `@keydris`
npm packages. See [npm-distribution.md](npm-distribution.md).

## Distribution at a glance

- **Base URL:** `https://get.keydris.com/keydris-cli` (CloudFront → S3).
- **Channels:** `stable` and `dev` — they are path prefixes in the same layout.
- **Platforms:** `darwin`/`linux` × `amd64`/`arm64`, built `CGO_ENABLED=0`
  (static, stdlib-only).

### Object layout (S3 key → URL)

All artifacts live under the `keydris-cli/` bucket prefix, served at the same
path on CloudFront:

```text
keydris-cli/install.sh                                 …/keydris-cli/install.sh
keydris-cli/<channel>/<version>/keydris-<os>-<arch>    …/keydris-cli/<channel>/<version>/keydris-<os>-<arch>
keydris-cli/<channel>/<version>/SHA256SUMS             …/keydris-cli/<channel>/<version>/SHA256SUMS
keydris-cli/<channel>/latest/…                         …/keydris-cli/<channel>/latest/…   (mutable pointer)
keydris-cli/<channel>/<version>/keydris.toml           …/keydris-cli/<channel>/<version>/keydris.toml
keydris-cli/<channel>/latest/keydris.toml              …/keydris-cli/<channel>/latest/keydris.toml
```

`latest/` and `install.sh` are published with `Cache-Control: max-age=60`;
versioned paths are immutable. Each publish invalidates `/keydris-cli/install.sh`
and `/keydris-cli/<channel>/latest/*` on CloudFront.

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

**Required repo settings** (Settings → Secrets and variables → Actions).
The values are environment-specific and live only in the repository settings:

| Kind | Name | Value |
| --- | --- | --- |
| Secret | `AWS_RELEASE_ROLE_ARN` | IAM role CI assumes via OIDC to publish |
| Variable | `KEYDRIS_RELEASE_BUCKET` | Target S3 artifacts bucket |
| Variable | `KEYDRIS_CLOUDFRONT_DISTRIBUTION_ID` | Distribution to invalidate |
| Variable | `AWS_REGION` | The bucket's region |

On npmjs.com, configure `keydrisLabs/keydris-cli` and workflow filename
`release.yml` as a trusted publisher for each of the seven `@keydris` packages,
and enable the `npm publish` action.

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
  S3_BUCKET=<artifacts-bucket> \
  DISTRIBUTION_ID=<distribution-id>      # optional: invalidates CloudFront
```

`VERSION` defaults to `git describe --tags --always --dirty`; override with
`make release VERSION=v0.1.0 …`.

## How users install

```bash
# stable
curl -fsSL https://get.keydris.com/keydris-cli/install.sh | bash

# dev — also drops a zero-config ~/.keydris.toml (dev control plane + IdP)
curl -fsSL https://get.keydris.com/keydris-cli/install.sh | KEYDRIS_CHANNEL=dev bash
```

Installer env: `PREFIX`, `KEYDRIS_CHANNEL` (`stable`|`dev`), `KEYDRIS_VERSION`
(default `latest`), `KEYDRIS_BASE_URL`. It verifies the download against
`SHA256SUMS` before installing. `keydris version` reports the installed build.

## Versioning

The version is stamped at link time:
`-ldflags "-X github.com/keydrisLabs/keydris-cli/internal/cli.Version=<v>"`
(handled by `make build` / `make dist`). Unstamped builds report `dev`.

## Notes / gotchas

- **Per-environment buckets.** Set `KEYDRIS_RELEASE_BUCKET` (and the matching
  role/distribution) per environment so each channel is served from the bucket
  intended for it.
- **Path prefix.** Artifacts live under `keydris-cli/` in the bucket, matching
  the publish role's `s3:prefix` scope and the CloudFront path — so the install
  URL is `…/keydris-cli/install.sh`. To switch to a clean root layout
  (`…/install.sh`), widen the IAM `ListBucket`/`PutObject` scope and drop the
  prefix from `install.sh` + the workflow + `Makefile` together.
- **npm provenance.** Trusted publishing works for private and public repos,
  but npm provenance attestations require a public repository — enable
  provenance in the package manifests once the repo is public.
- **macOS Gatekeeper.** `curl`-downloaded binaries aren't quarantined and run
  fine; a *browser* download would be blocked as "unidentified developer." The
  fix is Apple Developer ID signing + notarization.
