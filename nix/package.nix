{ lib
, buildGoModule
, stdenv
, installShellFiles
, releaseVersion
, revision
}:
assert builtins.match "[A-Za-z0-9][A-Za-z0-9._-]*" releaseVersion != null;
assert builtins.stringLength releaseVersion <= 128;
assert builtins.match "[A-Za-z0-9][A-Za-z0-9._-]*" revision != null;
buildGoModule {
  pname = "ollame";
  version = releaseVersion;
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../LICENSE
      ../go.mod
      ../go.sum
      ../cmd
      ../internal
    ];
  };
  vendorHash = "sha256-PmBNXQ8UDh5K/EamgrFxCN0fNXwBpYUJuRXcLWXK0gI=";
  subPackages = [ "cmd/ollame" ];
  env.CGO_ENABLED = "0";
  ldflags = [
    "-s"
    "-w"
    "-X main.version=${releaseVersion}"
    "-X main.commit=${revision}"
  ];
  # Integration and recorded-fixture checks run from the development checkout.
  doCheck = false;
  nativeBuildInputs = [ installShellFiles ];
  postInstall = ''
    install -Dm644 LICENSE $out/share/licenses/ollame/LICENSE
  '' + lib.optionalString (stdenv.buildPlatform.canExecute stdenv.hostPlatform) ''
    installShellCompletion --cmd ollame \
      --bash <($out/bin/ollame completion bash) \
      --zsh <($out/bin/ollame completion zsh) \
      --fish <($out/bin/ollame completion fish)
  '';
  doInstallCheck = stdenv.buildPlatform.canExecute stdenv.hostPlatform;
  installCheckPhase = ''
    runHook preInstallCheck
    $out/bin/ollame version
    $out/bin/ollame serve --help > /dev/null
    runHook postInstallCheck
  '';
  meta = {
    description = "Ollama-compatible API backed by an OpenAI-compatible upstream";
    homepage = "https://github.com/jpetrucciani/ollame";
    mainProgram = "ollame";
    license = lib.licenses.mit;
    platforms = lib.platforms.unix;
  };
}
