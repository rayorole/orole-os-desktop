import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/mygo", async () => (await import("./testing")).mygoMock);

import { ScreenChannel, fromBase64, toBase64 } from "./screenChannel";
import { b64, emit, listenerCount, resetSandbox, sandbox } from "./testing";

const bytesOf = (buffer: ArrayBuffer) => [...new Uint8Array(buffer)];
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

beforeEach(() => resetSandbox());

describe("screen channel", () => {
  it("fails the channel instead of dropping bytes when too many of its chunks arrive before the id is known", async () => {
    let resolve!: (id: string) => void;
    sandbox.openScreen.mockReturnValue(new Promise<string>((r) => { resolve = r; }));
    const ch = new ScreenChannel("b1");
    const onerror = vi.fn();
    const onclose = vi.fn();
    ch.onerror = onerror;
    ch.onclose = onclose;
    const opening = ch.open();
    for (let i = 0; i < 257; i++) emit("sandboxScreen", { id: "s1", data: b64([1]) });
    expect(onerror).not.toHaveBeenCalled(); // whose bytes they are is unknown until the id arrives
    resolve("s1");
    await opening;
    expect(onerror).toHaveBeenCalledTimes(1);
    expect(onclose).toHaveBeenCalledWith(expect.objectContaining({ code: 1006, wasClean: false }));
    expect(ch.readyState).toBe("closed");
    expect(sandbox.closeScreen).toHaveBeenCalledWith("s1");
  });

  it("does not fail on another screen's early chunks past the cap", async () => {
    let resolve!: (id: string) => void;
    sandbox.openScreen.mockReturnValue(new Promise<string>((r) => { resolve = r; }));
    const ch = new ScreenChannel("b1");
    const onerror = vi.fn();
    const got: number[][] = [];
    ch.onerror = onerror;
    ch.onmessage = (e) => got.push(bytesOf(e.data));
    const opening = ch.open();
    for (let i = 0; i < 300; i++) emit("sandboxScreen", { id: "other", data: b64([9]) });
    for (let i = 0; i < 10; i++) emit("sandboxScreen", { id: `x${i}`, data: b64([8]) }); // other screens still in the cap
    emit("sandboxScreen", { id: "s1", data: b64([82, 70, 66]) });
    resolve("s1");
    await opening;
    expect(onerror).not.toHaveBeenCalled();
    expect(ch.readyState).toBe("open");
    expect(got).toEqual([[82, 70, 66]]);
  });

  it("has every property noVNC's Websock.attach checks", () => {
    const ch = new ScreenChannel("b1");
    const props = [...Object.keys(ch), ...Object.getOwnPropertyNames(Object.getPrototypeOf(ch))];
    for (const p of ["send", "close", "binaryType", "onerror", "onmessage", "onopen", "protocol", "readyState"]) expect(props).toContain(p);
    expect(ch.readyState).toBe("connecting");
  });

  it("opens the screen, then replays bytes that arrived before the id was known and ignores other screens", async () => {
    let resolve!: (id: string) => void;
    sandbox.openScreen.mockReturnValue(new Promise<string>((r) => { resolve = r; }));
    const ch = new ScreenChannel("b1");
    const onopen = vi.fn();
    const got: number[][] = [];
    ch.onopen = onopen;
    ch.onmessage = (e) => got.push(bytesOf(e.data));
    const opening = ch.open();
    emit("sandboxScreen", { id: "s1", data: b64([82, 70, 66]) }); // the guest speaks first
    emit("sandboxScreen", { id: "other", data: b64([9]) });
    resolve("s1");
    await opening;
    expect(sandbox.openScreen).toHaveBeenCalledWith("b1");
    expect(ch.readyState).toBe("open");
    expect(onopen).toHaveBeenCalledTimes(1);
    emit("sandboxScreen", { id: "s1", data: b64([1, 2]) });
    expect(got).toEqual([[82, 70, 66], [1, 2]]);
  });

  it("queues bytes until noVNC assigns onmessage", async () => {
    sandbox.openScreen.mockResolvedValue("s1");
    const ch = new ScreenChannel("b1");
    await ch.open();
    emit("sandboxScreen", { id: "s1", data: b64([5, 6]) });
    const got: number[][] = [];
    ch.onmessage = (e) => got.push(bytesOf(e.data));
    expect(got).toEqual([[5, 6]]);
  });

  it("copies outgoing bytes at once and sends them in order, one call at a time", async () => {
    sandbox.openScreen.mockResolvedValue("s1");
    let release!: () => void;
    sandbox.screenInput.mockReturnValueOnce(new Promise<void>((r) => { release = r; })).mockResolvedValue(undefined);
    const ch = new ScreenChannel("b1");
    await ch.open();
    const reused = new Uint8Array([1, 2, 3, 4]);
    ch.send(new Uint8Array(reused.buffer, 0, 2));
    reused.set([7, 7], 0); // noVNC reuses its send buffer
    ch.send(new Uint8Array(reused.buffer, 2, 2));
    ch.send(new Uint8Array([5]));
    expect(sandbox.screenInput).toHaveBeenCalledTimes(1);
    expect([...fromBase64(sandbox.screenInput.mock.calls[0]![1] as string)]).toEqual([1, 2]);
    release();
    await flush();
    expect(sandbox.screenInput).toHaveBeenCalledTimes(2);
    expect([...fromBase64(sandbox.screenInput.mock.calls[1]![1] as string)]).toEqual([3, 4, 5]);
  });

  it("close ends the stream once, tells noVNC and stops listening", async () => {
    sandbox.openScreen.mockResolvedValue("s1");
    const ch = new ScreenChannel("b1");
    const onclose = vi.fn();
    ch.onclose = onclose;
    await ch.open();
    ch.close();
    ch.close();
    expect(sandbox.closeScreen).toHaveBeenCalledTimes(1);
    expect(sandbox.closeScreen).toHaveBeenCalledWith("s1");
    expect(onclose).toHaveBeenCalledTimes(1);
    expect(onclose.mock.calls[0]![0]).toMatchObject({ code: 1000, wasClean: true });
    expect(ch.readyState).toBe("closed");
    expect(listenerCount("sandboxScreen")).toBe(0);
  });

  it("reports the daemon closing the stream with its reason", async () => {
    sandbox.openScreen.mockResolvedValue("s1");
    const ch = new ScreenChannel("b1");
    const onclose = vi.fn();
    ch.onclose = onclose;
    await ch.open();
    emit("sandboxScreen", { id: "s1", closed: true, error: "the computer stopped" });
    expect(onclose.mock.calls[0]![0]).toMatchObject({ code: 1011, reason: "the computer stopped", wasClean: false });
    expect(sandbox.closeScreen).not.toHaveBeenCalled();
    expect(ch.closeReason).toBe("the computer stopped");
  });

  it("fails with the daemon's message when the screen cannot open", async () => {
    sandbox.openScreen.mockRejectedValue(new Error("not_found: the computer is off"));
    const ch = new ScreenChannel("b1");
    const onerror = vi.fn();
    const onclose = vi.fn();
    ch.onerror = onerror;
    ch.onclose = onclose;
    await ch.open();
    expect(onerror).toHaveBeenCalledTimes(1);
    expect(onclose.mock.calls[0]![0]).toMatchObject({ code: 1006, reason: "not_found: the computer is off" });
  });

  it("closes a screen that opened after the channel was closed", async () => {
    sandbox.openScreen.mockResolvedValue("late");
    const ch = new ScreenChannel("b1");
    const opening = ch.open();
    ch.close();
    await opening;
    expect(sandbox.closeScreen).toHaveBeenCalledWith("late");
  });

  it("round-trips base64 for large payloads", () => {
    const data = new Uint8Array(200_000).map((_, i) => i % 256);
    expect([...fromBase64(toBase64(data))]).toEqual([...data]);
  });
});
