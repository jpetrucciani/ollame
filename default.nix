{ pkgs ? import
    (fetchTarball {
      # nixup: pin=jpetrucciani/nix;
      name = "jpetrucciani-2026-09-10";
      url = "https://github.com/jpetrucciani/nix/archive/d610166be5017217d8e20ee5191da1e9678691ed.tar.gz";
      sha256 = "1hc07xhnxzmnv8ix9dhlcy83m9j2avvxvwww229zpzmsz5yxnad5";
    })
    { }
, releaseVersion ? "dev"
, revision ? "unknown"
, imageName ? "ollame"
}:
let
  name = "ollame";
  package = pkgs.callPackage ./nix/package.nix { inherit releaseVersion revision; };
  image = pkgs.callPackage ./nix/image.nix { inherit package imageName revision; };

  envVars = {
    NIXUP = "0.0.15";
    CGO_ENABLED = "0";
  };
  tools = with pkgs; {
    cli = [
      jfmt
      nixup
    ];
    go = [
      go
      go-tools
      gopls
    ];
    scripts = pkgs.lib.attrsets.attrValues scripts;
  };

  scripts = {
    publish = pkgs.callPackage ./nix/publish.nix { };
    fmt = pkgs.pog {
      name = "fmt";
      description = "format ollame Go sources";
      runtimeInputs = [ pkgs.go ];
      script = ''
        export CGO_ENABLED=0
        go fmt ./...
      '';
    };
    vet = pkgs.pog {
      name = "vet";
      description = "statically check ollame Go packages";
      runtimeInputs = [ pkgs.go ];
      script = ''
        export CGO_ENABLED=0
        go vet ./...
      '';
    };
    test = pkgs.pog {
      name = "test-ollame";
      description = "run ollame tests; forward Go test arguments after --";
      runtimeInputs = [ pkgs.go ];
      script = ''
        export CGO_ENABLED=0
        if (( $# )); then
          go test "$@"
        else
          go test ./...
        fi
      '';
    };
    build = pkgs.pog {
      name = "build-ollame";
      description = "build the static ollame binary into result-bin";
      runtimeInputs = [ pkgs.go ];
      script = ''
        export CGO_ENABLED=0
        go build -trimpath -o result-bin/ollame ./cmd/ollame
      '';
    };
  };
  paths = pkgs.lib.flatten [ (builtins.attrValues tools) ];
  env = pkgs.buildEnv {
    inherit name paths;
    buildInputs = paths;
  };
in
(env.overrideAttrs (old: {
  inherit name;
  env = (old.env or { }) // envVars;
}))
  // {
  inherit scripts package image;
}
