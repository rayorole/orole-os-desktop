# The bot computer. Agents see an ordinary Linux (nix-ld + envfs give FHS paths); Orole writes this file,
# agents never run nixos-rebuild (direct kernel boot, system.switch disabled).
{ config, lib, pkgs, modulesPath, orole-guest, ... }:

let
  # Chromium in the kiosk compositor. CDP listens on guest loopback only (orole-guest relays it over the
  # channel); the profile lives on the bot's disk so logins survive restarts. Restarted if it exits.
  browser = pkgs.writeShellScript "orole-browser" ''
    mkdir -p /home/bot/.config/orole-chromium
    while true; do
      ${pkgs.chromium}/bin/chromium \
        --ozone-platform=wayland \
        --user-data-dir=/home/bot/.config/orole-chromium \
        --remote-debugging-address=127.0.0.1 \
        --remote-debugging-port=9222 \
        --no-first-run --no-default-browser-check \
        --disable-gpu \
        --start-maximized \
        about:blank
      sleep 1
    done
  '';
in
{
  imports = [
    "${modulesPath}/profiles/minimal.nix"
    "${modulesPath}/profiles/qemu-guest.nix"
  ];

  system.stateVersion = "26.05";

  # Direct kernel boot: no bootloader, systemd initrd, no switch-to-configuration (perl-free activation).
  boot.loader.grub.enable = false;
  boot.initrd.systemd.enable = true;
  boot.initrd.availableKernelModules = [ "virtio_pci" "virtio_blk" "virtio_net" "virtio_console" "virtio_rng" "ext4" ];
  # The channel token arrives through QEMU fw_cfg, which the kernel only exposes in sysfs with qemu_fw_cfg
  # loaded (a module in the NixOS kernel). Loading it in the initrd makes it present before any stage-2
  # service, orole-guest included.
  boot.initrd.kernelModules = [ "qemu_fw_cfg" ];
  boot.kernelModules = [ "qemu_fw_cfg" ];
  boot.kernelParams = [ "console=ttyS0" "quiet" ];
  system.switch.enable = false;
  services.userborn.enable = true;
  documentation.enable = false;

  fileSystems."/" = { device = "/dev/disk/by-label/nixos"; fsType = "ext4"; autoResize = true; };

  networking.hostName = "orole-computer";
  networking.useNetworkd = true;
  systemd.network.networks."10-uplink" = { matchConfig.Name = "en* eth*"; networkConfig.DHCP = "ipv4"; };
  networking.firewall.enable = false;
  # QEMU user networking maps 10.0.2.2 (and fec0::2 over IPv6) to the host's loopback, where Orole and other
  # local services listen. Nothing in the guest needs it: orole-guest talks over virtio-serial, DNS is
  # 10.0.2.3. Only DHCP renewals (systemd-networkd, port 68, which the bot cannot bind) go to 10.0.2.2.
  # Root in the guest could undo this, and the bot has no sudo. The internet and the home LAN stay
  # reachable: a documented, accepted risk (spec, "Network").
  networking.nftables = {
    enable = true;
    tables.orole_host = {
      family = "inet";
      content = ''
        chain output {
          type filter hook output priority 0; policy accept;
          ip daddr 10.0.2.2 udp sport 68 udp dport 67 accept
          ip daddr 10.0.2.2 reject
          ip6 daddr fec0::2 reject
        }
      '';
    };
  };

  users.mutableUsers = false;
  # No login path by design: root and bot have locked passwords, no sshd, no sudo. Agents reach the VM only
  # through orole-guest; the debug image (debug.nix) adds root autologin on the serial console.
  users.allowNoPasswordLogin = true;
  users.users.root.hashedPassword = "!";
  users.users.bot = { isNormalUser = true; uid = 1000; home = "/home/bot"; createHome = true; hashedPassword = "!"; };
  security.sudo.enable = false;

  # Only root (orole-guest) may open the host channel; the kernel already makes fw_cfg files 0400 root, the
  # tmpfiles line keeps the token that way if that ever changes.
  services.udev.extraRules = ''
    SUBSYSTEM=="virtio-ports", ATTR{name}=="org.orole.guest.0", OWNER="root", GROUP="root", MODE="0600"
  '';
  systemd.tmpfiles.rules = [ "z /sys/firmware/qemu_fw_cfg/by_name/opt/orole/token/raw 0400 root root - -" ];

  # Desktop: cage on wlroots' headless backend (no GPU device), software rendering; wayvnc exports it on
  # guest loopback for orole-guest's screen relay.
  services.cage = {
    enable = true;
    user = "bot";
    program = browser;
    environment = {
      WLR_BACKENDS = "headless";
      WLR_RENDERER = "pixman";
      WLR_LIBINPUT_NO_DEVICES = "1";
      WLR_HEADLESS_OUTPUTS = "1";
    };
  };
  # Restart forever: the default start limit (5 starts in 10 s) would leave a crashing service down.
  systemd.services."cage-tty1" = {
    unitConfig.StartLimitIntervalSec = 0;
    serviceConfig = { Restart = "always"; RestartSec = 2; };
  };
  systemd.services.wayvnc = {
    description = "VNC server for the bot computer's screen (guest loopback only)";
    after = [ "cage-tty1.service" ];
    wantedBy = [ "graphical.target" ];
    unitConfig.StartLimitIntervalSec = 0;
    serviceConfig = {
      User = "bot";
      Environment = [ "XDG_RUNTIME_DIR=/run/user/1000" "WAYLAND_DISPLAY=wayland-0" ];
      ExecStart = "${pkgs.wayvnc}/bin/wayvnc 127.0.0.1 5900";
      Restart = "always";
      RestartSec = 2;
    };
  };
  systemd.defaultUnit = "graphical.target";

  # Runs as root: it powers the VM off and owns the channel. It runs commands and terminals as bot.
  systemd.services.orole-guest = {
    description = "Orole guest agent (virtio-serial org.orole.guest.0)";
    after = [ "systemd-modules-load.service" "systemd-udevd.service" ];
    wantedBy = [ "multi-user.target" ];
    unitConfig.StartLimitIntervalSec = 0;
    serviceConfig = { ExecStart = lib.getExe orole-guest; Restart = "always"; RestartSec = 1; };
  };

  # Fonts for Chromium (without fontconfig and a font set, pages render as boxes).
  fonts.fontconfig.enable = true;
  fonts.packages = with pkgs; [ dejavu_fonts noto-fonts ];

  environment.systemPackages = with pkgs; [
    python3 uv nodejs git curl jq ripgrep chromium
    coreutils findutils gnugrep gnused gawk which file unzip zip gnutar xz
  ];
  programs.nix-ld = {
    enable = true;
    libraries = with pkgs; [ stdenv.cc.cc.lib zlib openssl glib nss nspr libgcc ];
  };
  services.envfs.enable = true;
  nix.settings.experimental-features = [ "nix-command" "flakes" ];
  services.journald.settings.Journal.SystemMaxUse = "64M";
}
