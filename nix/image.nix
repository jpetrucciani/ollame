{ dockerTools
, cacert
, package
, imageName
, revision
}:
dockerTools.buildLayeredImage {
  name = imageName;
  tag = package.version;
  contents = [
    package
    cacert
  ];
  config = {
    Labels = {
      "org.opencontainers.image.title" = "ollame";
      "org.opencontainers.image.version" = package.version;
      "org.opencontainers.image.revision" = revision;
    };
    User = "65532:65532";
    WorkingDir = "/";
    Entrypoint = [ "${package}/bin/ollame" ];
    Cmd = [
      "serve"
      "--config"
      "/etc/ollame/ollame.toml"
    ];
    Env = [ "SSL_CERT_FILE=${cacert}/etc/ssl/certs/ca-bundle.crt" ];
    ExposedPorts = {
      "11434/tcp" = { };
      "9434/tcp" = { };
    };
    StopSignal = "SIGTERM";
  };
}
