# orole-guest (cmd/orole-guest) built from this repository. vendorHash must change whenever go.sum (or the set
# of imported modules) does: set it to lib.fakeHash, run `nix build ./guest#orole-guest`, and paste the "got:"
# hash.
{ lib, buildGo127Module, src, version }:

buildGo127Module {
  pname = "orole-guest";
  inherit version;
  src = lib.fileset.toSource {
    root = src;
    fileset = lib.fileset.fileFilter
      (f: f.hasExt "go" || f.name == "go.mod" || f.name == "go.sum" || f.name == "mygo.json")
      src;
  };
  vendorHash = "sha256-zXOxVCCy6WftvQ9o1bxJ8t4TXa06/PYhDOI8IEmqASs=";
  subPackages = [ "cmd/orole-guest" ];
  env.CGO_ENABLED = "0";
  ldflags = [ "-s" "-w" "-X main.version=${version}" ];
  doCheck = false; # tested by `go test` in CI
  meta.mainProgram = "orole-guest";
}
