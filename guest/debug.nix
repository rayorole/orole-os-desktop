# Debug variant only (never released): root shell on the serial console for manual checks.
{ lib, ... }:
{
  services.getty.autologinUser = lib.mkForce "root";
  boot.kernelParams = lib.mkForce [ "console=ttyS0" ];
}
