{
  description = "A basic Go web server setup";

  inputs = {
    nixpkgs.url = "nixpkgs/nixos-unstable";
    utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, utils }:
    utils.lib.eachSystem [
      "x86_64-linux"
      "aarch64-linux"
      "x86_64-darwin"
      "aarch64-darwin"
    ] (system:
      let
        pkgs =
          import nixpkgs {
            inherit system;
            overlays = [
              (final: prev: {
                go = prev.go_1_19;
                buildGoModule = prev.buildGo119Module;
              })
            ];
          };
      in {
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [ go gopls gotools go-tools ];
        };
      });
}
