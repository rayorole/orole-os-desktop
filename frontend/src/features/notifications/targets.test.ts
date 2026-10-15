import { afterEach, describe, expect, it, vi } from "vitest";
import { useChat } from "@/stores/chat";
import { openTarget, parseTarget } from "./targets";

const original = useChat.getState().open;
afterEach(() => useChat.setState({ open: original }));

describe("notification targets", () => {
  it("reads computer targets as a bot's computer", () => {
    expect(parseTarget("computer:b1")).toEqual({ type: "computer", id: "b1" });
    expect(parseTarget("bot:b1")).toEqual({ type: "bot", id: "b1" });
    expect(parseTarget("computer:")).toBeNull();
    expect(parseTarget("vm:b1")).toBeNull();
  });

  it("opens the bot when its computer needs the user", () => {
    const open = vi.fn();
    useChat.setState({ open });
    expect(openTarget("computer:b1")).toBe(true);
    expect(open).toHaveBeenCalledWith("b1");
  });
});
