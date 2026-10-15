# Building the bot computer image

The guest image is a NixOS system built from `guest/flake.nix`. CI builds released images
(`.github/workflows/guest-image.yml`, tag `guest-v<guest/VERSION>`); this page is for building and testing one
locally on Windows through WSL. Nix does not run on Windows itself.

What it holds and which guest facts other code relies on: `docs/architecture/bot-computer.md`, "Guest image".

## One-time WSL setup (Ubuntu)

```bash
sh <(curl --proto '=https' --tlsv1.2 -L https://nixos.org/nix/install) --daemon --yes
sudo tee -a /etc/nix/nix.conf <<'EOF'
experimental-features = nix-command flakes
system-features = nixos-test benchmark big-parallel kvm
EOF
sudo systemctl restart nix-daemon
git config --global --add safe.directory '*'   # the repo lives on /mnt/c, owned by the Windows user
ls -l /dev/kvm                                 # make-disk-image is much faster with KVM (WSL2 nests it on Windows 11)
```

## Build

Nix only sees files tracked by git: `git add` new files under `guest/` first.

```bash
cd /mnt/c/code/projects/.orole-worktrees/orole-os/196-sandbox-core
nix build ./guest#image -L --out-link guest/result          # release variant
nix build ./guest#image-debug -L --out-link guest/result-debug
mkdir -p /mnt/c/Users/$WINUSER/AppData/Local/Temp/orole-guest && cp -L guest/result-debug/* "$_"/
```

The output holds `vmlinuz`, `initrd`, `cmdline`, `base.qcow2` (must stay under GitHub's 2 GiB asset limit) and
`SHA256SUMS`.

When `go.sum` (or the set of modules the repository imports) changes, the `orole-guest` build fails with a hash
mismatch. Set `vendorHash = lib.fakeHash;` in `guest/orole-guest.nix`, run `nix build ./guest#orole-guest`, and paste
the `got: sha256-…` value. Without Nix, the hash is the NAR hash of the `vendor` folder that `go mod vendor` writes
for a copy of the repository's `*.go`, `go.mod`, `go.sum` and `mygo.json` files.

To move nixpkgs: change the revision in `guest/flake.nix`, run `nix flake lock ./guest`, and bump `guest/VERSION`.
Use a nixos-unstable channel head (`curl https://channels.nixos.org/nixos-unstable/git-revision`): Hydra built
and cached it, so Chromium comes from cache.nixos.org instead of building from source for hours. The release's
`store-paths.xz` (next to `git-revision` under `releases.nixos.org`) lists the cached paths if you want to check.

## Boot it on Windows

With QEMU unpacked (the spike's copy, or an installed runtime under `<data>\sandbox\runtime\`):

```powershell
$q = "$env:TEMP\orole-spike\qemu"; $img = "$env:TEMP\orole-guest"
& "$q\qemu-img.exe" create -f qcow2 -F qcow2 -b "$img\base.qcow2" "$env:TEMP\dbg.qcow2" 10G
& "$q\qemu-system-x86_64.exe" -nodefaults -no-user-config -machine q35 -accel whpx,kernel-irqchip=off -cpu host `
  -m 2048 -smp 2 -display none -vga none -L "$q\share" -kernel "$img\vmlinuz" -initrd "$img\initrd" `
  -append "$(Get-Content -Raw $img\cmdline) console=ttyS0" `
  -drive "if=none,id=root,format=qcow2,file=$env:TEMP\dbg.qcow2" -device virtio-blk-pci,drive=root `
  -netdev user,id=net0 -device virtio-net-pci,netdev=net0,romfile= -device virtio-rng-pci `
  -fw_cfg name=opt/orole/token,string=test -serial stdio
```

The debug image logs root in on the serial console. Quit QEMU with `poweroff`. The checks to run there are listed
under "Guest image checks" in `docs/architecture/bot-computer.md`.
