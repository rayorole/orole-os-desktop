import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/mygo", async () => (await import("./builder/testing")).mygoMock);

import { useChat } from "@/stores/chat";
import { BotActions } from "./BotHeader";
import { bot } from "./builder/testing";

afterEach(() => { cleanup(); useChat.setState({ bots: [], selectedBotId: null, pane: null, settingsOpen: false }); });

describe("bot title bar actions", () => {
  it("shows a Computer button only for bots with a computer, toggling the Computer pane", () => {
    useChat.setState({ bots: [bot("b1", { config: { ...bot("x").config, computer: true } })], selectedBotId: "b1", pane: null });
    render(<BotActions />);
    fireEvent.click(screen.getByRole("button", { name: "Show computer" }));
    expect(useChat.getState().pane).toEqual({ kind: "computer", id: "b1" });
    fireEvent.click(screen.getByRole("button", { name: "Hide computer" }));
    expect(useChat.getState().pane).toBeNull();
  });

  it("has no Computer button for other bots", () => {
    useChat.setState({ bots: [bot("b2")], selectedBotId: "b2", pane: null });
    render(<BotActions />);
    expect(screen.queryByRole("button", { name: "Show computer" })).toBeNull();
    expect(screen.getByRole("button", { name: "Show bot settings" })).toBeInTheDocument();
  });
});
