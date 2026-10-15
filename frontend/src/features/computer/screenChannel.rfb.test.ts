import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/mygo", async () => (await import("./testing")).mygoMock);

import { ScreenChannel, fromBase64 } from "./screenChannel";
import { b64, emit, resetSandbox, sandbox } from "./testing";

/** A 2D context whose every method is a no-op: jsdom has no canvas, and this test is about the protocol. */
function fakeContext(): unknown {
  return new Proxy({}, { get: (_t, prop) => (prop === "getImageData" ? () => ({ data: new Uint8ClampedArray(4) }) : () => {}), set: () => true });
}

const ascii = (s: string) => [...s].map((c) => c.charCodeAt(0));
/** Every byte the client has sent so far, in order. */
const sent = () => sandbox.screenInput.mock.calls.flatMap((call) => [...fromBase64(call[1] as string)]);

const live: Array<[{ disconnect: () => void }, HTMLElement]> = [];

beforeEach(() => {
  resetSandbox();
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => fakeContext() as never);
});
afterEach(() => {
  for (const [rfb, target] of live.splice(0)) { try { rfb.disconnect(); } catch { /* already disconnected */ } target.remove(); }
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("screen channel with noVNC's RFB", () => {
  it("drives a real RFB through the handshake to connected", async () => {
    const { default: RFB } = await import("@novnc/novnc");
    sandbox.openScreen.mockResolvedValue("s1");
    const target = document.createElement("div");
    document.body.append(target);
    const ch = new ScreenChannel("b1");
    const rfb = new RFB(target, ch);
    live.push([rfb, target]);
    const connected = vi.fn();
    rfb.addEventListener("connect", connected);
    await ch.open();

    emit("sandboxScreen", { id: "s1", data: b64(ascii("RFB 003.008\n")) });
    await vi.waitFor(() => expect(sent()).toEqual(ascii("RFB 003.008\n")));

    emit("sandboxScreen", { id: "s1", data: b64([1, 1]) }); // one security type: None
    await vi.waitFor(() => expect(sent().slice(12)).toEqual([1]));

    emit("sandboxScreen", { id: "s1", data: b64([0, 0, 0, 0]) }); // SecurityResult OK
    await vi.waitFor(() => expect(sent().slice(13)).toEqual([1])); // ClientInit, shared

    const name = ascii("bot");
    const pixelFormat = [32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0];
    emit("sandboxScreen", { id: "s1", data: b64([0, 64, 0, 48, ...pixelFormat, 0, 0, 0, name.length, ...name]) });
    await vi.waitFor(() => expect(connected).toHaveBeenCalledTimes(1));
    // After ServerInit the client sends SetPixelFormat (message type 0) first.
    await vi.waitFor(() => expect(sent()[14]).toBe(0));

    rfb.disconnect();
    expect(sandbox.closeScreen).toHaveBeenCalledWith("s1");
  });
});
