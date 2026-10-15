import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/mygo", async () => (await import("./testing")).mygoMock);

import { emit, resetSandbox, sandbox } from "./testing";
import { useComputer, useViewerLease, VIEWER_REFRESH_MS } from "./useComputer";

beforeEach(() => {
  resetSandbox();
  sandbox.availability.mockResolvedValue({ ready: true });
  sandbox.status.mockResolvedValue({ botId: "b1", state: "off", takenOver: false });
  sandbox.handoff.mockResolvedValue({ botId: "b1", pending: false });
});
afterEach(() => { cleanup(); vi.useRealTimers(); });

describe("useComputer", () => {
  it("loads availability and status, then follows this bot's status events only", async () => {
    const { result } = renderHook(() => useComputer("b1"));
    await waitFor(() => expect(result.current.status?.state).toBe("off"));
    expect(result.current.availability).toEqual({ ready: true });
    act(() => emit("sandboxStatus", { botId: "b2", state: "running", takenOver: false }));
    expect(result.current.status?.state).toBe("off");
    act(() => emit("sandboxStatus", { botId: "b1", state: "starting", takenOver: false }));
    expect(result.current.status?.state).toBe("starting");
  });

  it("follows availability, runtime, setup and this bot's handoff events", async () => {
    const { result } = renderHook(() => useComputer("b1"));
    await waitFor(() => expect(result.current.status?.state).toBe("off"));
    act(() => emit("sandboxAvailability", { ready: false, reason: "runtime-missing", detail: "d" }));
    expect(result.current.availability).toEqual({ ready: false, reason: "runtime-missing", detail: "d" });
    act(() => emit("sandboxRuntime", { ready: false, installing: true, step: "qemu", done: 1, total: 2, error: "" }));
    expect(result.current.runtime?.step).toBe("qemu");
    act(() => emit("sandboxSetup", { running: false, rebootRequired: true }));
    expect(result.current.setup?.rebootRequired).toBe(true);
    act(() => emit("sandboxHandoff", { botId: "b2", pending: true }));
    expect(result.current.handoff?.pending).toBe(false);
    act(() => emit("sandboxHandoff", { botId: "b1", pending: true, reason: "captcha" }));
    expect(result.current.handoff).toMatchObject({ pending: true, reason: "captcha" });
  });

  it("starts setup and download and keeps what they answer", async () => {
    sandbox.enable.mockResolvedValue({ running: true, rebootRequired: false });
    sandbox.install.mockResolvedValue({ ready: false, installing: true, done: 0, total: 0 });
    const { result } = renderHook(() => useComputer("b1"));
    await act(async () => { await result.current.enable(); await result.current.install(); });
    expect(result.current.setup?.running).toBe(true);
    expect(result.current.runtime?.installing).toBe(true);
  });

  it("ignores a refresh that a later one or a live event has overtaken, and one for a previous bot", async () => {
    let resolveOld!: (s: unknown) => void;
    sandbox.status.mockReturnValueOnce(new Promise((r) => { resolveOld = r; }));
    const { result, rerender } = renderHook(({ id }) => useComputer(id), { initialProps: { id: "b1" } });
    act(() => emit("sandboxStatus", { botId: "b1", state: "running", takenOver: false }));
    await act(async () => { resolveOld({ botId: "b1", state: "off", takenOver: false }); });
    expect(result.current.status?.state).toBe("running");

    let resolveB1!: (s: unknown) => void;
    sandbox.status.mockReturnValueOnce(new Promise((r) => { resolveB1 = r; }));
    act(() => { void result.current.refresh(); });
    sandbox.status.mockResolvedValue({ botId: "b2", state: "paused", takenOver: false });
    rerender({ id: "b2" });
    await waitFor(() => expect(result.current.status?.botId).toBe("b2"));
    await act(async () => { resolveB1({ botId: "b1", state: "stopping", takenOver: false }); });
    expect(result.current.status?.botId).toBe("b2");
  });

  it("clears the load error when a later refresh succeeds", async () => {
    sandbox.availability.mockRejectedValueOnce(new Error("internal: daemon unavailable"));
    const { result } = renderHook(() => useComputer("b1"));
    await waitFor(() => expect(result.current.error).toBe("daemon unavailable"));
    await act(async () => { await result.current.refresh(); });
    expect(result.current.error).toBeNull();
    expect(result.current.status?.state).toBe("off");
  });

  it("clears the other step's error when setup or download starts", async () => {
    const { result } = renderHook(() => useComputer("b1"));
    await waitFor(() => expect(result.current.status).not.toBeNull());
    act(() => emit("sandboxRuntime", { ready: false, installing: false, done: 0, total: 0, error: "download failed" }));
    await act(async () => { await result.current.enable(); });
    expect(result.current.runtime?.error).toBe("");
    act(() => emit("sandboxSetup", { running: false, rebootRequired: false, error: "setup failed" }));
    await act(async () => { await result.current.install(); });
    expect(result.current.setup?.error).toBe("");
  });

  it("keeps the load error for the tab to show", async () => {
    sandbox.availability.mockRejectedValue(new Error("internal: daemon unavailable"));
    const { result } = renderHook(() => useComputer("b1"));
    await waitFor(() => expect(result.current.error).toBe("daemon unavailable"));
  });
});

describe("useViewerLease", () => {
  it("renews well under the 150 s expiry, and stops on unmount", () => {
    expect(VIEWER_REFRESH_MS).toBeLessThan(75_000);
    vi.useFakeTimers();
    const { unmount } = renderHook(() => useViewerLease("b1"));
    expect(sandbox.setViewer).toHaveBeenLastCalledWith("b1", true);
    vi.advanceTimersByTime(VIEWER_REFRESH_MS);
    expect(sandbox.setViewer).toHaveBeenCalledTimes(2);
    expect(sandbox.setViewer).toHaveBeenLastCalledWith("b1", true);
    unmount();
    expect(sandbox.setViewer).toHaveBeenLastCalledWith("b1", false);
    vi.advanceTimersByTime(3 * VIEWER_REFRESH_MS);
    expect(sandbox.setViewer).toHaveBeenCalledTimes(3);
  });

  it("pauses while the window is hidden and resumes when visible", () => {
    vi.useFakeTimers();
    const vis = vi.spyOn(document, "visibilityState", "get");
    vis.mockReturnValue("visible");
    const { unmount } = renderHook(() => useViewerLease("b1"));
    vis.mockReturnValue("hidden");
    act(() => { document.dispatchEvent(new Event("visibilitychange")); });
    expect(sandbox.setViewer).toHaveBeenLastCalledWith("b1", false);
    const n = sandbox.setViewer.mock.calls.length;
    vi.advanceTimersByTime(3 * VIEWER_REFRESH_MS);
    expect(sandbox.setViewer).toHaveBeenCalledTimes(n);
    vis.mockReturnValue("visible");
    act(() => { document.dispatchEvent(new Event("visibilitychange")); });
    expect(sandbox.setViewer).toHaveBeenLastCalledWith("b1", true);
    unmount();
    vis.mockRestore();
  });
});
