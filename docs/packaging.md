# Nix package and container

The existing `default.nix` still exposes the development environment by default.
It also exports `package` and `image`, using the same pinned package set:

```sh
nix-build -A package -o result-package
./result-package/bin/ollame version
nix-build -A image -o result-image
docker load < result-image
```

The package builds with CGO disabled and installs Bash, Zsh, and Fish completions
on native builds. Its install check runs the built version and help commands.
Direct Nix builds default to `dev (unknown)`. Override `releaseVersion` and
`revision` with `--argstr` to stamp a specific build. The publishing helper below
sets these automatically and includes matching OCI version/revision labels.

The container is tagged `ollame:dev`. It contains the package and CA certificates,
uses UID/GID 65532, and defaults to reading `/etc/ollame/ollame.toml`. Mount a
configuration with absolute container paths for secrets, for example:

```toml
[server]
listen = ":11434"
admin_listen = ":9434"
[upstream]
base_url = "http://litellm:4000/v1"
api_key_file = "/run/secrets/upstream-key"
[auth]
mode = "required"
tokens_file = "/run/secrets/tokens"
```

The upstream hostname must be reachable from the container's network. Localhost
inside the container refers to the container itself. Grant UID/GID 65532 read
access to mounted configuration and secret files; keep the secrets out of the
image and Nix store.

```sh
docker run --rm --name ollame \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  --stop-timeout 70 \
  -p 127.0.0.1:11434:11434 -p 127.0.0.1:9434:9434 \
  -v "$PWD/container.toml:/etc/ollame/ollame.toml:ro" \
  -v "$PWD/.secrets:/run/secrets:ro" \
  ollame:dev
```

The 70-second stop timeout covers the default drain delay, shutdown grace and
final-error margin. Keep the admin port private when changing published addresses.

The package and image have been built successfully on Linux amd64. The installed
binary is statically linked; all three completion files are present. The image
was loaded into Docker and served authenticated model listing and chat against
real LiteLLM/llama-server while running as UID/GID 65532 with a read-only filesystem.
The CA bundle was present and readable, debug remained disabled, and graceful
shutdown exited successfully. Other build platforms and cluster deployment remain
separate validation work.

## Build and publish to GHCR

The project shell exposes `publish-ollame`. You can also build just the helper:

```sh
nix-build -A scripts.publish -o result-publish
./result-publish/bin/publish-ollame --release-version 0.1.0 --action build
```

This builds the Nix image, loads `ghcr.io/jpetrucciani/ollame:0.1.0` into Docker,
and runs its version command. To publish that tag, authenticate Docker to GHCR
with an account/token authorized to push this package, then run:

```sh
docker login ghcr.io
./result-publish/bin/publish-ollame --release-version 0.1.0
```

The default action is `push`; `--action build` avoids registry writes. Only the
specified tag is pushed, with no automatic `latest` tag. The image targets the
build machine's architecture; this is not a multi-platform manifest publisher.

The revision defaults to Git HEAD and gets a `-dirty` suffix when the checkout has
changes. A checkout without commits reports `unknown` (or `unknown-dirty`). Use
`--revision <commit>` for an explicitly identified source archive.
`OLLAME_RELEASE_VERSION` and `OLLAME_BUILD_COMMIT` provide environment equivalents.
The helper reads Git state but does not commit, tag, or otherwise change it.

`GET /version` on either listener reports the ollame build:

```json
{ "version": "0.1.0", "commit": "<commit>", "ollama_version": "0.34.0" }
```

This build metadata endpoint is public. `/api/version` retains Ollama's version
shape and compatibility value. API responses also include `X-Ollame-Version`
and `X-Ollame-Commit`. Zerolog emits JSON by default with service, version, commit,
request correlation, status, and timing fields; `log.format="text"` selects the
console renderer. Reloaded loggers retain the build fields.

The publishing helper's local build/load path, stamped endpoints, JSON logs, and
image labels were exercised with `0.1.0-dev`. No GHCR push was performed during
that validation; registry access depends on your Docker login and package rights.

## GitHub Actions releases

The lint workflow runs the Nix `fmt`, `vet`, and internal test helpers on branch pushes and pull
requests. Formatting changes fail CI. The release workflow also runs lint before
building artifacts.

Push a tag beginning with `v` (for example `v0.1.0`), or publish a GitHub release,
to run the release workflow. It builds native Nix packages on Ubuntu x86_64 and
macOS ARM64 (`macos-15`, per the [GitHub runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)) and attaches these files to the matching release:

- `ollame-x86_64-linux`, a fully static Linux binary.
- `ollame-aarch64-darwin`, a CGO-disabled macOS binary using Apple system libraries.
- `SHA256SUMS`, checksums for both binaries.
- `LICENSE`, the project MIT license.

The Nix package and container also include the project license at
`share/licenses/ollame/LICENSE` within the package output.

Downloaded binaries need `chmod +x`. macOS builds are not Developer ID signed or
notarized. Both artifacts are stamped with the exact tag and checked-out commit.
If a tag push has no release yet, the workflow creates one with generated notes.
Existing release assets are replaced on reruns; immutable published releases
cannot accept replacement assets. Tag-push and release-published runs for the same
tag are serialized.

After both binaries build, CI uses the existing publish helper to build and check
the scratch-based **linux/amd64** image, then pushes
`ghcr.io/jpetrucciani/ollame:<exact-tag>`. It does not publish a `latest` tag or a
Linux ARM64 image. For `v0.1.0`, the image tag is also `v0.1.0`.

Publishing uses the repository's `GITHUB_TOKEN` with `contents: write` and
`packages: write`; no personal access token is needed. If the GHCR package already
exists, grant this repository Actions access in the package settings. Package
visibility is managed separately in GHCR. The workflows must be present in the
commit being released. No workflow runs or registry publication are performed by
merely adding these files locally.
