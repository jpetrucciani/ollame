{ pog
, nix
, docker
, git
}:
pog {
  name = "publish-ollame";
  description = "Build a stamped image and push it to ghcr.io/jpetrucciani/ollame";
  runtimeInputs = [
    nix
    docker
    git
  ];
  flags = [
    {
      name = "release-version";
      short = "";
      default = "dev";
      envVar = "OLLAME_RELEASE_VERSION";
      description = "Image tag and binary version, for example 0.1.0";
    }
    {
      name = "revision";
      short = "";
      envVar = "OLLAME_BUILD_COMMIT";
      description = "Commit stamp; defaults to local Git HEAD or unknown";
    }
    {
      name = "action";
      short = "";
      default = "push";
      description = "push builds, loads and pushes; build builds and loads locally only";
    }
  ];
  script = ''
    set -euo pipefail
    if [[ ! -f nix/package.nix || ! -f go.mod ]]; then
      echo "run publish-ollame from the ollame checkout" >&2
      exit 1
    fi
    case "$action" in build|push) ;; *) echo "action must be build or push" >&2; exit 1 ;; esac
    if [[ ! "$release_version" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]; then
      echo "release-version must be a valid image tag" >&2
      exit 1
    fi
    if [[ -z "$revision" ]]; then
      revision=$(git rev-parse --verify HEAD 2>/dev/null || echo unknown)
      if [[ -n "$(git status --porcelain 2>/dev/null)" ]]; then
        revision="$revision-dirty"
      fi
    fi
    if [[ ! "$revision" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
      echo "revision contains unsupported characters" >&2
      exit 1
    fi
    image_ref="ghcr.io/jpetrucciani/ollame:$release_version"
    nix-build -A image -o result-image \
      --argstr releaseVersion "$release_version" \
      --argstr revision "$revision" \
      --argstr imageName ghcr.io/jpetrucciani/ollame
    docker load -i result-image
    docker run --rm --network none "$image_ref" version
    if [[ "$action" == push ]]; then
      docker push "$image_ref"
    fi
    echo "$image_ref"
  '';
}
