{
  description = "Orole bot computer: NixOS guest image (kernel, initrd, kernel command line, base disk)";

  # Pinned to a nixos-unstable channel head (channels.nixos.org/nixos-unstable/git-revision): Hydra built and
  # cached it, so Chromium and the kernel come from cache.nixos.org. Has go_1_27. Update the same way, run
  # `nix flake lock ./guest`, and bump guest/VERSION.
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/55bfb4d7437bd4e09ab7cba58a43bd68eb310044";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      lib = nixpkgs.lib;
      pkgs = nixpkgs.legacyPackages.${system};
      version = lib.strings.trim (builtins.readFile ./VERSION);
      orole-guest = pkgs.callPackage ./orole-guest.nix { inherit version; src = ../.; };

      # mkImage builds the four files the host downloads: vmlinuz, initrd, cmdline (holds init=<toplevel>/init,
      # which changes every build) and base.qcow2 (compressed qcow2, ext4 without a partition table, used
      # read-only as the backing file of every bot's overlay).
      mkImage = name: extraModules:
        let
          nixos = lib.nixosSystem {
            inherit system;
            specialArgs = { inherit orole-guest; };
            modules = [ ./configuration.nix ] ++ extraModules;
          };
          cfg = nixos.config;
          disk = import "${nixpkgs}/nixos/lib/make-disk-image.nix" {
            inherit pkgs lib;
            config = cfg;
            format = "qcow2-compressed";
            partitionTableType = "none";
            label = "nixos";
            diskSize = "auto";
            additionalSpace = "1024M";
            copyChannel = false;
            installBootLoader = false;
          };
          cmdline = "init=${cfg.system.build.toplevel}/init ${toString cfg.boot.kernelParams}";
        in
        pkgs.runCommand "${name}-${version}" { } ''
          mkdir -p $out
          cp ${cfg.system.build.kernel}/${cfg.system.boot.loader.kernelFile} $out/vmlinuz
          cp ${cfg.system.build.initialRamdisk}/${cfg.system.boot.loader.initrdFile} $out/initrd
          cp ${disk}/nixos.qcow2 $out/base.qcow2
          printf '%s' ${lib.escapeShellArg cmdline} > $out/cmdline
          cd $out && sha256sum vmlinuz initrd cmdline base.qcow2 > SHA256SUMS
        '';
    in
    {
      packages.${system} = {
        inherit orole-guest;
        image = mkImage "orole-guest-image" [ ];
        image-debug = mkImage "orole-guest-image-debug" [ ./debug.nix ];
        default = self.packages.${system}.image;
      };
    };
}
