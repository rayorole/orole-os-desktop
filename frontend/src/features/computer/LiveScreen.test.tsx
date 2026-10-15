import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { rfbs } = vi.hoisted(() => ({ rfbs: [] as Array<EventTarget & { target: HTMLElement; channel: unknown; options: unknown; viewOnly: boolean; scaleViewport: boolean; resizeSession: boolean; disconnect: () => void; focus: () => void; blur: () => void }> }));

vi.mock("@novnc/novnc", async () => {
  const { vi: v } = await import("vitest");
  class FakeRFB extends EventTarget {
    target: HTMLElement;
    channel: unknown;
    options: unknown;
    viewOnly = false;
    scaleViewport = false;
    resizeSession = true;
    disconnect = v.fn();
    focus = v.fn();
    blur = v.fn();
    constructor(target: HTMLElement, channel: unknown, options: unknown) {
      super();
      this.target = target;
      this.channel = channel;
      this.options = options;
      rfbs.push(this as never);
    }
  }
  return { default: FakeRFB };
});
vi.mock("@/mygo", async () => (await import("./testing")).mygoMock);

import { LiveScreen } from "./LiveScreen";
import { ScreenChannel } from "./screenChannel";
import { resetSandbox, sandbox } from "./testing";

beforeEach(() => { resetSandbox(); rfbs.length = 0; sandbox.openScreen.mockResolvedValue("s1"); });
afterEach(cleanup);

describe("live screen", () => {
  it("draws the bot's screen through a screen channel, view-only, scaled and shared", async () => {
    render(<LiveScreen botId="b1" interactive={false} />);
    await act(async () => {});
    expect(rfbs).toHaveLength(1);
    const rfb = rfbs[0]!;
    expect(rfb.channel).toBeInstanceOf(ScreenChannel);
    expect((rfb.channel as ScreenChannel).botId).toBe("b1");
    expect(rfb.options).toEqual({ shared: true });
    expect(rfb.viewOnly).toBe(true);
    expect(rfb.scaleViewport).toBe(true);
    expect(rfb.resizeSession).toBe(false);
    expect(screen.getByRole("region", { name: "Live screen" })).toContainElement(rfb.target);
    expect(sandbox.openScreen).toHaveBeenCalledWith("b1");
    expect(screen.getByText("Connecting to the screen…")).toBeInTheDocument();
    act(() => { rfb.dispatchEvent(new Event("connect")); });
    expect(screen.queryByText("Connecting to the screen…")).toBeNull();
  });

  it("accepts input only while the user has taken over", async () => {
    const { rerender } = render(<LiveScreen botId="b1" interactive={false} />);
    await act(async () => {});
    const rfb = rfbs[0]!;
    act(() => { rfb.dispatchEvent(new Event("connect")); });
    rerender(<LiveScreen botId="b1" interactive />);
    expect(rfb.viewOnly).toBe(false);
    expect(rfb.focus).toHaveBeenCalled();
    rerender(<LiveScreen botId="b1" interactive={false} />);
    expect(rfb.viewOnly).toBe(true);
    expect(rfb.blur).toHaveBeenCalled();
  });

  it("focuses the screen when it connects during a takeover", async () => {
    render(<LiveScreen botId="b1" interactive />);
    await act(async () => {});
    const rfb = rfbs[0]!;
    expect(rfb.focus).not.toHaveBeenCalled();
    act(() => { rfb.dispatchEvent(new Event("connect")); });
    expect(rfb.focus).toHaveBeenCalled();
  });

  it("ignores events from a screen it has replaced", async () => {
    render(<LiveScreen botId="b1" interactive={false} />);
    await act(async () => {});
    act(() => { rfbs[0]!.dispatchEvent(new Event("disconnect")); });
    fireEvent.click(screen.getByRole("button", { name: "Reconnect" }));
    await act(async () => {});
    act(() => { rfbs[1]!.dispatchEvent(new Event("connect")); });
    act(() => { rfbs[0]!.dispatchEvent(new Event("disconnect")); });
    expect(screen.queryByText(/The screen disconnected/)).toBeNull();
  });

  it("says why the screen disconnected when the stream gave a reason", async () => {
    sandbox.openScreen.mockRejectedValue(new Error("bad_request: the computer is not running"));
    render(<LiveScreen botId="b1" interactive={false} />);
    await act(async () => {});
    act(() => { rfbs[0]!.dispatchEvent(new Event("disconnect")); });
    expect(screen.getByRole("status")).toHaveTextContent("The screen disconnected: the computer is not running");
  });

  it("says when the screen disconnected and reconnects on request", async () => {
    render(<LiveScreen botId="b1" interactive={false} />);
    await act(async () => {});
    act(() => { rfbs[0]!.dispatchEvent(new Event("disconnect")); });
    expect(screen.getByText("The screen disconnected.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reconnect" }));
    await act(async () => {});
    expect(rfbs).toHaveLength(2);
    expect(rfbs[0]!.disconnect).toHaveBeenCalled();
  });

  it("disconnects and closes the screen when it goes away", async () => {
    const { unmount } = render(<LiveScreen botId="b1" interactive={false} />);
    await act(async () => {});
    unmount();
    expect(rfbs[0]!.disconnect).toHaveBeenCalled();
    expect(sandbox.closeScreen).toHaveBeenCalledWith("s1");
  });
});
