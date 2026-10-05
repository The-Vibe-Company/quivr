# Release images

Release-please opens a release pull request on `main` from Conventional Commit
messages (the repository's squash-merged Commitizen PR titles). The coordinator
merges it when a release is wanted. Its version file, manifest, changelog, tag
and GitHub prerelease all belong to one distribution release. Workers never
merge release pull requests or create release tags.

## Configure release automation

Set the repository Actions secret `RELEASE_PLEASE_TOKEN` to a fine-grained PAT
or GitHub App token with Contents, Issues and Pull requests write access to this
repository. Run this locally as the repository administrator. This example was
not run in this workspace and is not part of release automation:

```sh
gh secret set RELEASE_PLEASE_TOKEN --repo The-Vibe-Company/quivr
```

The command prompts for the value; never put it in a file tracked by Git.
`GITHUB_TOKEN` cannot replace it: events it creates do not trigger the release
PR's CI or the image workflow. See the [release-please credential guidance](https://github.com/googleapis/release-please-action#other-actions-on-release-please-prs).
For an expiring App token, use `actions/create-github-app-token` in the release
workflow and pass its output to release-please instead of the PAT secret.

`release-please-config.json` selects the simple release type and prerelease
versioning. `.release-please-manifest.json` and `version.txt` start at
`2.0.0-alpha.0`; the first release PR proposes `2.0.0-alpha.1`. Features, fixes
and breaking changes increment the alpha counter while on `2.0.0-alpha.N`.
`bootstrap-sha` excludes history before this distribution release series.
The plugin engine compatibility version remains `0.2.0`, and the API remains
`v0`; these contracts are independent of the distribution version.

## Publish a release

Merging the release PR causes release-please to publish its GitHub prerelease.
Only that `release: published` event runs `.github/workflows/release-images.yml`.
The workflow refuses a tag outside `v2.0.0-alpha.N` or a version that differs
from the checked-out manifest and `version.txt`. It:

1. Discovers first-party plugin manifests and their Go or Python runtimes.
2. Builds `linux/amd64` images and pushes only their immutable digests.
3. Checks the real images as UID/GID 10001 with a read-only root and `/tmp` tmpfs.
4. Signs and verifies each digest with cosign and GitHub OIDC.
5. Attaches `images.txt` (signed digests) and release-please's `CHANGELOG.md` to
   the existing release. Its release notes already contain the changelog.
6. Creates version/full `sha-<commit>` tags and advances `latest-alpha` without
   changing the signed digests. Older reruns do not move that alias backwards.

Rolling aliases update separately for each package; registries cannot promote
multiple packages atomically. Deploy a shared release by its version or the
signed digests in `images.txt`, rather than combining `latest-alpha` tags.

GHCR package names use manifest ids: `quivr` for the engine and
`quivr-plugin-<id>` for each plugin. For example, `quivr-plugin-core.ingest`
and `quivr-plugin-connector.rss`. Set each new GHCR package's visibility to
public in its package settings after the first publication; GHCR initially
creates private packages. The image source label links them to this repository.

The engine uses `quivr.Dockerfile`; plugin targets are `go-plugin`,
`python-plugin` and `core-ingest` in `plugin.Dockerfile`. The latter includes
the hash-pinned tokenizer. Linux arm64 is not published: its tokenizer wheel
lock is not defined yet. Runtime images contain no compiler or package installer.
The runtime smoke script also serves as a local check for a built image.

Operator pulling, configuration and signature commands live in
[Deploy and configure Quivr](https://docs.quivr.thevibecompany.co/run-quivr/deploy).
The Railway demo's image switch is a separate deployment change. SBOM and
vulnerability scanning are separate release-pipeline work.
