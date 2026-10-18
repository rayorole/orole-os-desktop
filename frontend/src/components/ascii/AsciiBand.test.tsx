import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";

const lib = vi.hoisted(() => ({
  mount: vi.fn(),
  load: { "night-coast": vi.fn(), "deep-reef": vi.fn() } as Record<string, Mock>,
}));
vi.mock("@/lib/backend", () => ({ inDesktop: false, previewKey: "ascii-band-test" }));
vi.mock("ascii.rest", () => ({ mount: lib.mount, load: lib.load }));

import { AsciiBand } from "./AsciiBackground";
import { defaults, useSettings } from "@/stores/settings";
import { useUI } from "@/stores/ui";

interface Mounted { name: string; fps: number }
/** The arguments of mount(el, piece, { fps, motion }), as the band passed them. */
type MountCall = [HTMLElement, { meta: { name: string } }, { fps: number }];

const piece = (name: string) => ({ meta: { name }, default: () => () => "" });
const settle = () => act(async () => {
  const { promise, resolve } = Promise.withResolvers<void>();
  setTimeout(resolve, 0);
  await promise;
});
const mounted = (): Mounted[] => (lib.mount.mock.calls as MountCall[]).map(([, p, options]) => ({ name: p.meta.name, fps: options.fps }));

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} }));
  for (const [id, spy] of Object.entries(lib.load)) spy.mockReset().mockResolvedValue(piece(id));
  lib.mount.mockReset().mockReturnValue(() => {});
  useUI.setState({ windowFocused: true });
  useSettings.setState({ loaded: true, ascii: { ...defaults.ascii }, activeAsciiScene: "night-coast", accessibility: { reduceMotion: "system" } });
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("AsciiBand", () => {
  it("loads and mounts only the selected scene, and switches by loading just the new one", async () => {
    render(<AsciiBand />);
    await settle();
    expect(lib.load["night-coast"]).toHaveBeenCalledTimes(1);
    expect(lib.load["deep-reef"]).not.toHaveBeenCalled();
    expect(mounted()).toEqual([{ name: "night-coast", fps: 12 }]);

    act(() => useSettings.setState({ activeAsciiScene: "deep-reef" }));
    await settle();
    expect(lib.load["deep-reef"]).toHaveBeenCalledTimes(1);
    expect(mounted().at(-1)).toEqual({ name: "deep-reef", fps: 12 });
  });

  it("stops the old scene when the selection changes, so one scene runs at a time", async () => {
    const stops = [vi.fn(), vi.fn()];
    lib.mount.mockReset().mockReturnValueOnce(stops[0]).mockReturnValueOnce(stops[1]);
    render(<AsciiBand />);
    await settle();
    act(() => useSettings.setState({ activeAsciiScene: "deep-reef" }));
    await settle();
    expect(stops[0]).toHaveBeenCalledTimes(1);
    expect(stops[1]).not.toHaveBeenCalled();
  });

  it("draws nothing and loads nothing when the background is off", async () => {
    useSettings.setState({ ascii: { ...defaults.ascii, enabled: false } });
    const view = render(<AsciiBand />);
    await settle();
    expect(view.container.firstChild).toBeNull();
    expect(lib.load["night-coast"]).not.toHaveBeenCalled();
    expect(lib.mount).not.toHaveBeenCalled();
  });

  it("shows a still frame when the user asks for reduced motion", async () => {
    useSettings.setState({ accessibility: { reduceMotion: "on" } });
    const view = render(<AsciiBand />);
    await settle();
    expect(mounted()).toEqual([{ name: "night-coast", fps: 0 }]);
    expect(view.getByTestId("ascii-bg")).toHaveAttribute("data-motion", "reduced-motion");
  });

  it("follows the operating system's reduced-motion preference", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("reduce"), media: query, addEventListener() {}, removeEventListener() {} }));
    render(<AsciiBand />);
    await settle();
    expect(mounted()).toEqual([{ name: "night-coast", fps: 0 }]);
  });

  it("pauses to a still frame while the window is unfocused and resumes on focus", async () => {
    render(<AsciiBand />);
    await settle();
    expect(mounted().at(-1)?.fps).toBe(12);
    act(() => useUI.setState({ windowFocused: false }));
    await settle();
    expect(mounted().at(-1)?.fps).toBe(0);
    act(() => useUI.setState({ windowFocused: true }));
    await settle();
    expect(mounted().at(-1)?.fps).toBe(12);
  });

  it("is decorative: hidden from assistive technology and never takes pointer input", async () => {
    const view = render(<AsciiBand />);
    await settle();
    const band = view.getByTestId("ascii-bg");
    expect(band).toHaveAttribute("aria-hidden", "true");
    expect(band).toHaveClass("pointer-events-none");
  });
});
