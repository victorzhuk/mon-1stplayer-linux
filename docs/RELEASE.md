# Release runbook

## Prerequisites

- Publish the source on GitHub `master` and require green CI for the release commit.
- Configure repository secrets `AUR_USERNAME`, `AUR_EMAIL`, and `AUR_SSH_PRIVATE_KEY`.
- Register the corresponding SSH public key with the AUR account that can publish `mon-1stplayer` and `mon-1stplayer-bin`.

The publish job inside the reusable `aur-publish.yml` workflow uses the `aur` GitHub Environment. The caller job cannot use an environment because it invokes a reusable workflow; the current `AUR_USERNAME`, `AUR_EMAIL`, and `AUR_SSH_PRIVATE_KEY` values are repository secrets and remain available to the caller. If the publication structure is ever changed to support environment-scoped secrets, move these values into the `aur` environment and add required reviewers.

## Cut a release

Replace `X.Y.Z` with the chosen version in the steps below.

1. Move the entries under `[Unreleased]` in `CHANGELOG.md` to `## [X.Y.Z] - YYYY-MM-DD`, using today's date. For a release today, use `2026-10-26`. Keep an empty `[Unreleased]` section above it.
2. Update the changelog links: `[Unreleased]` points to `https://github.com/victorzhuk/mon-1stplayer-linux/compare/vX.Y.Z...HEAD`. Add `[X.Y.Z]` comparing the previous tag with `vX.Y.Z`. For the first release, link to `https://github.com/victorzhuk/mon-1stplayer-linux/releases/tag/vX.Y.Z`.
3. Commit as `chore(release): bump version to X.Y.Z`.
4. Push `master` and wait for green CI on that commit:

   ```sh
   git push origin master
   ```

5. Create the tag on that release commit and push it:

   ```sh
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```

Stable recipes are rendered from templates. No manual `pkgver` edit is needed.

## Job graph

Tags matching `vX.Y.Z` start this sequence:

1. Run the existing CI checks.
2. Build the Go binary and deterministically stage the release archive and checksum.
3. Verify any existing GitHub release assets against the build hash, then upload missing assets.
4. Call the AUR publisher with the build hash: prepare stable package metadata without AUR secrets, then publish in a separate job.

A failed stage prevents its dependent stages from running. GitHub release publication and AUR publication are separate effects. Check both results before treating a release as complete.

## Assets

The GitHub release contains:

- `mon-1stplayer-x86_64-linux.tar.gz`
- `mon-1stplayer-x86_64-linux.tar.gz.sha256`

The tarball has one root directory, `mon-1stplayer-VERSION-x86_64-linux/`, where `VERSION` is the tag without `v`:

```text
bin/mon-1stplayer
deploy/mon-1stplayer.service
deploy/mon-1stplayer.sysusers
deploy/70-mon-1stplayer.rules
deploy/mon-1stplayer.conf
LICENSE
README.md
```

Release staging is deterministic for the same release inputs. Before completing missing uploads, the release job verifies any already-existing assets against the build hash. Existing assets are never overwritten. A mismatch requires a new tag.

## AUR publication

The preparation job runs in the `archlinux:base` container without AUR secrets. It downloads the source and binary archives, calculates their SHA-256 hashes, and renders:

| Package | Template |
| --- | --- |
| `mon-1stplayer` | `pkg/archlinux/stable/PKGBUILD.in` |
| `mon-1stplayer-bin` | `pkg/archlinux/bin/PKGBUILD.in` |

It replaces `@VERSION@` with the tag version and `@SHA256@` with the corresponding downloaded archive hash, then generates `.SRCINFO`. For the binary archive, preparation checks the download against the hash passed from the build job. The release-hosted checksum sidecar is not the trust root for AUR metadata. Stable templates are not directly publishable.

Only the separate publish job receives `AUR_USERNAME`, `AUR_EMAIL`, and `AUR_SSH_PRIVATE_KEY`. It uses a directly pinned AUR ed25519 host key and does not execute package code. For each stable package, it stages exactly `PKGBUILD`, `.SRCINFO`, and `LICENSE`, then pushes `HEAD:master` without force. Both publish matrix legs run independently with `fail-fast: false`.

On reruns:

- A candidate older than the currently published `pkgver` is a superseded-release no-op.
- Identical content is a no-op.
- Changed content for the same `pkgver` is refused and requires a new tag or `pkgrel` increment.

Package installation never starts or enables the service.

The VCS recipe at `pkg/archlinux/PKGBUILD` defines `mon-1stplayer-git`. It is separate from stable publication and is not updated automatically for version-only upstream changes.
