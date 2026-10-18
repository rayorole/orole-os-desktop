import { beforeEach, describe, expect, it, vi } from "vitest";

const packaged = vi.hoisted(() => ({
  "night-coast": vi.fn(async () => ({ meta: { name: "night coast" }, default: () => () => "" })),
  "deep-reef": vi.fn(),
}));
vi.mock("ascii.rest", () => ({ load: packaged }));

import { loadScene } from "./scene-loader";

beforeEach(() => { for (const spy of Object.values(packaged)) spy.mockClear(); });

describe("loadScene", () => {
  it("imports a vendored scene on demand and leaves the packaged ones alone", async () => {
    const piece = await loadScene("salt-flat")!;
    expect(piece.meta).toMatchObject({ name: "salt flat", cols: 200, rows: 100 });
    expect(typeof piece.default).toBe("function");
    expect(Object.values(packaged).every((spy) => spy.mock.calls.length === 0)).toBe(true);
  });

  it("asks the package for a packaged scene once, however often it is requested", async () => {
    const first = loadScene("night-coast")!;
    expect(loadScene("night-coast")).toBe(first);
    await first;
    expect(packaged["night-coast"]).toHaveBeenCalledTimes(1);
    expect(packaged["deep-reef"]).not.toHaveBeenCalled();
  });

  it("returns nothing for an id that is neither vendored nor packaged", () => {
    expect(loadScene("not-a-scene")).toBeUndefined();
    expect(loadScene("../pieces/salt-flat")).toBeUndefined();
  });

  it("forgets a failed load so the next request tries again", async () => {
    packaged["deep-reef"].mockRejectedValueOnce(new Error("chunk missing"));
    await expect(loadScene("deep-reef")).rejects.toThrow("chunk missing");
    await Promise.resolve();
    packaged["deep-reef"].mockResolvedValueOnce({ meta: {}, default: () => () => "" });
    await expect(loadScene("deep-reef")).resolves.toBeTruthy();
    expect(packaged["deep-reef"]).toHaveBeenCalledTimes(2);
  });
});
