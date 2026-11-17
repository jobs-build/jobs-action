{
  description = "jobs-action";
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

    systems.url = "github:nix-systems/default";

  };

  outputs = { self, nixpkgs, systems, ... }@inputs:
    let
      eachSystem = f:
        nixpkgs.lib.genAttrs (import systems)
        (system: f system nixpkgs.legacyPackages.${system});
    in {

      devShells = eachSystem (system: pkgs: {
        default = pkgs.mkShell {
          hardeningDisable = [ "all" ];

          packages = with pkgs; [ go gh ];
        };
      });
    };
}
