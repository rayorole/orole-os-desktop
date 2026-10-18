import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { SCENES } from "@/components/ascii/scenes";

const bridge = vi.hoisted(() => ({ all: vi.fn(), set: vi.fn(), loaders: {} as Record<string, Mock>, mount: vi.fn() }));
vi.mock("@/lib/backend", () => ({ inDesktop: true, previewKey: "scene-grid-test" }));
vi.mock("@/mygo", () => ({
  Settings: { all: bridge.all, set: bridge.set },
  events: { settingsChanged: { on: () => {} } },
}));
// Every scene the packaged library offers is a spy: opening the picker must call none of them.
vi.mock("ascii.rest", () => ({
  load: new Proxy({}, { get: (_, id: string) => (bridge.loaders[id] ??= vi.fn(async () => ({ meta: {}, default: () => () => "" }))) }),
  mount: bridge.mount,
}));

import { ScenePicker } from "./ScenePicker";
import { useSettings, defaults } from "@/stores/settings";

const options = () => screen.getAllByRole("option");
const choose = (name: RegExp) => fireEvent.click(screen.getByRole("option", { name }));
const lastWrite = () => bridge.set.mock.calls.at(-1) as [string, Record<string, unknown>];

beforeEach(() => {
  // jsdom has no layout: give the scroller the size the list is measured against.
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(400);
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(640);
  bridge.all.mockResolvedValue({});
  bridge.set.mockResolvedValue(undefined);
  bridge.mount.mockClear();
  for (const spy of Object.values(bridge.loaders)) spy.mockClear();
  useSettings.setState({ loaded: true, ascii: { ...defaults.ascii }, activeAsciiScene: defaults.ascii.scene });
});
afterEach(() => { cleanup(); bridge.set.mockReset(); });

describe("Background scene picker", () => {
  it("lists None first, then every scene, and marks the saved one as selected", () => {
    render(<ScenePicker />);
    const listbox = screen.getByRole("listbox", { name: "Background scene" });
    const first = within(listbox).getAllByRole("option")[0]!;
    expect(first).toHaveAccessibleName(/None/);
    expect(first).toHaveAttribute("aria-posinset", "1");
    expect(first).toHaveAttribute("aria-setsize", String(SCENES.length + 1));
    const selected = options().filter((o) => o.getAttribute("aria-selected") === "true");
    expect(selected).toHaveLength(1);
    expect(selected[0]).toHaveAccessibleName(/Night coast/);
  });

  it("mounts only the rows in view, never the whole catalogue", () => {
    render(<ScenePicker />);
    expect(options().length).toBeLessThan(SCENES.length + 1);
    expect(screen.queryByRole("option", { name: /Amer fort/ })).toBeNull();
  });

  it("draws every thumbnail as a lazy image and no canvas, and runs no scene code", () => {
    render(<ScenePicker />);
    const images = document.querySelectorAll("[role=option] img");
    expect(images.length).toBeGreaterThan(0);
    for (const img of images) {
      expect(img).toHaveAttribute("loading", "lazy");
      expect(img.getAttribute("src")).toMatch(/previews\/[a-z-]+\.png/);
    }
    expect(document.querySelector("canvas")).toBeNull();
    expect(bridge.mount).not.toHaveBeenCalled();
    expect(Object.values(bridge.loaders).every((spy) => spy.mock.calls.length === 0)).toBe(true);
  });

  it("saves the chosen scene, turns the background on and stops random startup", () => {
    useSettings.setState({ ascii: { ...defaults.ascii, enabled: false, randomOnStartup: true } });
    render(<ScenePicker />);
    choose(/Alpine dawn/);
    expect(lastWrite()[0]).toBe("ascii");
    expect(lastWrite()[1]).toMatchObject({ scene: "alpine-dawn", enabled: true, randomOnStartup: false });
    expect(screen.getByRole("option", { name: /Alpine dawn/ })).toHaveAttribute("aria-selected", "true");
  });

  it("None switches the background off but keeps the scene for when it is turned back on", () => {
    useSettings.setState({ ascii: { ...defaults.ascii, scene: "deep-reef" }, activeAsciiScene: "deep-reef" });
    render(<ScenePicker />);
    choose(/None/);
    expect(lastWrite()[1]).toMatchObject({ enabled: false, scene: "deep-reef" });
    expect(screen.getByRole("option", { name: /None/ })).toHaveAttribute("aria-selected", "true");
    expect(options().filter((o) => o.getAttribute("aria-selected") === "true")).toHaveLength(1);
    choose(/Deep reef/);
    expect(lastWrite()[1]).toMatchObject({ enabled: true, scene: "deep-reef" });
  });

  it("starts from None when the saved settings have the background off", () => {
    useSettings.setState({ ascii: { ...defaults.ascii, enabled: false } });
    render(<ScenePicker />);
    expect(screen.getByRole("option", { name: /None/ })).toHaveAttribute("aria-selected", "true");
  });

  it("selects nothing while an uploaded image is the background", () => {
    const custom = { name: "photo.png", pixels: btoa("\0".repeat(200 * 100 * 3)) };
    useSettings.setState({ ascii: { ...defaults.ascii, customBackground: custom } });
    render(<ScenePicker />);
    expect(options().some((o) => o.getAttribute("aria-selected") === "true")).toBe(false);
  });

  it("moves with the arrow keys without choosing, and reaches tiles that were not mounted", () => {
    render(<ScenePicker />);
    const listbox = screen.getByRole("listbox", { name: "Background scene" });
    const start = options().find((o) => o.tabIndex === 0)!;
    expect(options().filter((o) => o.tabIndex === 0)).toHaveLength(1);
    start.focus();
    fireEvent.keyDown(start, { key: "ArrowRight" });
    expect(document.activeElement).toHaveAccessibleName(/Aurora fjord/);
    expect(bridge.set).not.toHaveBeenCalled();
    fireEvent.keyDown(document.activeElement!, { key: "End" });
    expect(document.activeElement).toHaveAccessibleName(/Amer fort/);
    expect(document.activeElement).toHaveAttribute("aria-posinset", String(SCENES.length + 1));
    expect(within(listbox).getAllByRole("option").filter((o) => o.tabIndex === 0)).toHaveLength(1);
    fireEvent.keyDown(document.activeElement!, { key: "Home" });
    expect(document.activeElement).toHaveAccessibleName(/None/);
    expect(bridge.set).not.toHaveBeenCalled();
  });

  it("persists the choice across a reload of the saved settings", async () => {
    render(<ScenePicker />);
    const start = options().find((o) => o.tabIndex === 0)!;
    start.focus();
    fireEvent.keyDown(start, { key: "End" });
    fireEvent.click(document.activeElement!);
    const [key, saved] = lastWrite();
    expect(saved).toMatchObject({ scene: "amer-fort", enabled: true });

    useSettings.setState({ ascii: { ...defaults.ascii }, activeAsciiScene: defaults.ascii.scene });
    bridge.all.mockResolvedValue({ [key]: saved });
    await act(async () => { await useSettings.getState().load(); });
    expect(useSettings.getState().activeAsciiScene).toBe("amer-fort");
    cleanup();
    render(<ScenePicker />);
    const picked = options().find((o) => o.getAttribute("aria-selected") === "true");
    expect(picked).toHaveAccessibleName(/Amer fort/);
  });

  it("ignores clicks while preferences are still loading", () => {
    useSettings.setState({ loaded: false });
    render(<ScenePicker />);
    choose(/Alpine dawn/);
    expect(bridge.set).not.toHaveBeenCalled();
  });
});
