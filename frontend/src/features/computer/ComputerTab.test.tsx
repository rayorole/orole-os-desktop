import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/mygo", async () => (await import("./testing")).mygoMock);
vi.mock("./LiveScreen", () => ({
  LiveScreen: ({ botId, interactive }: { botId: string; interactive: boolean }) => <div data-testid="live-screen" data-bot={botId} data-interactive={String(interactive)} />,
}));
vi.mock("@/features/terminals/TerminalPane", () => ({
  TerminalPane: ({ id }: { id: string }) => <div data-testid="terminal" data-id={id} />,
}));

import { ComputerTab } from "./ComputerTab";
import { emit, resetSandbox, sandbox } from "./testing";

const running = (takenOver = false) => ({ botId: "b1", state: "running", takenOver });
const view = () => render(<ComputerTab botId="b1" onClose={() => {}} />);

beforeEach(() => {
  resetSandbox();
  sandbox.availability.mockResolvedValue({ ready: true });
  sandbox.status.mockResolvedValue({ botId: "b1", state: "off", takenOver: false });
  sandbox.openTerminal.mockResolvedValue({ id: "t1", state: "running" });
});
afterEach(cleanup);

describe("computer tab setup", () => {
  it("explains Windows Hypervisor Platform with the daemon's detail, enables it and shows the restart step", async () => {
    sandbox.availability.mockResolvedValueOnce({ ready: false, reason: "whp-disabled", detail: "HypervisorPlatform: Disabled" })
      .mockResolvedValue({ ready: false, reason: "reboot-required" });
    sandbox.enable.mockResolvedValue({ running: false, rebootRequired: true });
    view();
    expect(await screen.findByRole("heading", { name: "Turn on Windows Hypervisor Platform" })).toBeInTheDocument();
    expect(screen.getByText("HypervisorPlatform: Disabled")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Enable" }));
    await waitFor(() => expect(sandbox.enable).toHaveBeenCalledTimes(1));
    expect(await screen.findByRole("heading", { name: "Restart Windows to finish" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Check again" })).toBeInTheDocument();
  });

  it("waits for the Windows prompt while setup runs and shows its error", async () => {
    sandbox.availability.mockResolvedValue({ ready: false, reason: "whp-disabled" });
    sandbox.enable.mockResolvedValue({ running: true, rebootRequired: false });
    view();
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    expect(await screen.findByRole("button", { name: "Enabling…" })).toBeDisabled();
    act(() => emit("sandboxSetup", { running: false, rebootRequired: false, error: "internal: the prompt was declined" }));
    expect(screen.getByRole("alert")).toHaveTextContent("the prompt was declined");
    expect(screen.getByRole("alert")).not.toHaveTextContent("internal:");
    expect(screen.getByRole("button", { name: "Enable" })).toBeEnabled();
  });

  it("downloads the runtime with progress and offers Download again with the reason when it fails", async () => {
    sandbox.availability.mockResolvedValue({ ready: false, reason: "runtime-missing" });
    sandbox.install.mockResolvedValue({ ready: false, installing: true, done: 0, total: 0 });
    view();
    fireEvent.click(await screen.findByRole("button", { name: "Download" }));
    expect(await screen.findByRole("button", { name: "Downloading…" })).toBeDisabled();
    act(() => emit("sandboxRuntime", { ready: false, installing: true, step: "image", done: 50, total: 200 }));
    expect(screen.getByText("Linux system image")).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "25");
    act(() => emit("sandboxRuntime", { ready: false, installing: false, done: 0, total: 0, error: "internal: hash mismatch for qemu.zip" }));
    expect(screen.getByRole("alert")).toHaveTextContent("hash mismatch for qemu.zip");
    expect(screen.queryByRole("progressbar")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    await waitFor(() => expect(sandbox.install).toHaveBeenCalledTimes(2));
  });

  it("only re-checks when the reason has no fix Orole can run", async () => {
    sandbox.availability.mockResolvedValue({ ready: false, reason: "something-new" });
    view();
    fireEvent.click(await screen.findByRole("button", { name: "Check again" }));
    await waitFor(() => expect(sandbox.availability).toHaveBeenCalledTimes(2));
    expect(sandbox.enable).not.toHaveBeenCalled();
    expect(sandbox.install).not.toHaveBeenCalled();
  });

  it("offers no action when the user must fix virtualization themselves", async () => {
    sandbox.availability.mockResolvedValue({ ready: false, reason: "virtualization-off" });
    view();
    expect(await screen.findByRole("heading", { name: "Virtualization is not available" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Enable|Download|Check again/ })).toBeNull();
  });

  it("states plainly what the computer can reach", async () => {
    sandbox.availability.mockResolvedValue({ ready: false, reason: "runtime-missing" });
    view();
    await screen.findByRole("heading", { name: "Download the bot computer" });
    expect(screen.getByText(/can reach the internet, your home network and anything on this PC/)).toBeInTheDocument();
    expect(screen.getByText(/the bot can use them/)).toBeInTheDocument();
  });
});

describe("computer tab states", () => {
  it("says the computer is off, then starting, then shows the screen and terminal when it runs", async () => {
    view();
    expect(await screen.findByText(/It starts when the bot uses it\./)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /start/i })).toBeNull();
    expect(screen.queryByTestId("live-screen")).toBeNull();
    expect(sandbox.openTerminal).not.toHaveBeenCalled();
    act(() => emit("sandboxStatus", { botId: "b1", state: "starting", takenOver: false }));
    expect(screen.getByText("Starting computer…")).toBeInTheDocument();
    act(() => emit("sandboxStatus", running()));
    expect(screen.getByTestId("live-screen")).toHaveAttribute("data-interactive", "false");
    expect(await screen.findByTestId("terminal")).toHaveAttribute("data-id", "t1");
    expect(sandbox.openTerminal).toHaveBeenCalledWith("b1", 100, 24);
    expect(screen.getByRole("button", { name: "Take over" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Done" })).toBeNull();
    expect(screen.getByText(/can reach the internet, your home network/)).toBeInTheDocument();
  });

  it("shows why the computer failed", async () => {
    sandbox.status.mockResolvedValue({ botId: "b1", state: "failed", takenOver: false, error: "the computer failed to start" });
    view();
    expect(await screen.findByText("the computer failed to start")).toBeInTheDocument();
  });

  it("shows the terminal error and retries", async () => {
    sandbox.status.mockResolvedValue(running());
    sandbox.openTerminal.mockRejectedValueOnce(new Error("internal: no shell"));
    view();
    expect(await screen.findByRole("alert")).toHaveTextContent("no shell");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByTestId("terminal")).toHaveAttribute("data-id", "t1");
  });

  it("marks the bot as watched while the tab is open", async () => {
    const { unmount } = view();
    await screen.findByText(/It starts when the bot uses it/);
    expect(sandbox.setViewer).toHaveBeenCalledWith("b1", true);
    unmount();
    expect(sandbox.setViewer).toHaveBeenLastCalledWith("b1", false);
  });
});

describe("computer tab takeover", () => {
  it("takes over, makes the screen interactive, and hands back with Done and a note", async () => {
    sandbox.status.mockResolvedValueOnce(running(false)).mockResolvedValueOnce(running(true)).mockResolvedValue(running(false));
    view();
    fireEvent.click(await screen.findByRole("button", { name: "Take over" }));
    await waitFor(() => expect(sandbox.takeOver).toHaveBeenCalledWith("b1"));
    expect(await screen.findByRole("button", { name: "Done" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Take over" })).toBeNull();
    expect(screen.getByTestId("live-screen")).toHaveAttribute("data-interactive", "true");
    expect(screen.getByText(/The bot can't use the computer until you click Done\. If you leave this tab or minimise the window for about 2½ minutes, the bot gets it back automatically\./)).toBeInTheDocument();
    expect(screen.queryByText(/The bot waits/)).toBeNull();
    const note = screen.getByRole("textbox", { name: "Note for the bot" });
    expect(note).toHaveAttribute("maxLength", "500");
    fireEvent.change(note, { target: { value: "  Signed in to GitHub  " } });
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() => expect(sandbox.release).toHaveBeenCalledWith("b1", "Signed in to GitHub"));
    expect(await screen.findByRole("button", { name: "Take over" })).toBeInTheDocument();
    expect(screen.getByTestId("live-screen")).toHaveAttribute("data-interactive", "false");
  });

  it("sends Done without a note", async () => {
    sandbox.status.mockResolvedValue(running(true));
    view();
    fireEvent.click(await screen.findByRole("button", { name: "Done" }));
    await waitFor(() => expect(sandbox.release).toHaveBeenCalledWith("b1", ""));
  });

  it("shows the bot's reason when it asks the user to take over", async () => {
    sandbox.status.mockResolvedValue(running(false));
    sandbox.handoff.mockResolvedValue({ botId: "b1", pending: true, reason: "Sign in to GitHub, then click Done." });
    view();
    expect(await screen.findByText("Sign in to GitHub, then click Done.")).toBeInTheDocument();
    expect(screen.getByText("The bot needs you")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Take over" })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Take over" }));
    await waitFor(() => expect(sandbox.takeOver).toHaveBeenCalledWith("b1"));
  });

  it("says the bot is waiting only while a handoff is pending", async () => {
    sandbox.status.mockResolvedValue(running(true));
    sandbox.handoff.mockResolvedValue({ botId: "b1", pending: true });
    view();
    expect(await screen.findByText(/The bot waits until you click Done/)).toBeInTheDocument();
  });

  it("follows a handoff request that arrives while the tab is open", async () => {
    sandbox.status.mockResolvedValue(running(false));
    view();
    await screen.findByRole("button", { name: "Take over" });
    act(() => emit("sandboxHandoff", { botId: "b1", pending: true }));
    expect(screen.getByText("The bot needs you")).toBeInTheDocument();
    expect(screen.getByText("The bot asked you to take over its computer.")).toBeInTheDocument();
  });

  it("shows the daemon's refusal", async () => {
    sandbox.status.mockResolvedValue(running(false));
    sandbox.takeOver.mockRejectedValue(new Error("bad_request: the computer is not running"));
    view();
    fireEvent.click(await screen.findByRole("button", { name: "Take over" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("the computer is not running");
  });

  it.each([
    ["the user took over", { botId: "b1", pending: false }],
    ["the bot asked", { botId: "b1", pending: true, reason: "Sign in" }],
  ])("keeps the takeover when the pane closes (%s)", async (_, handoff) => {
    sandbox.status.mockResolvedValue(running(true));
    sandbox.handoff.mockResolvedValue(handoff);
    const { unmount } = view();
    await screen.findByRole("button", { name: "Done" });
    unmount();
    await act(async () => {});
    expect(sandbox.release).not.toHaveBeenCalled();
  });

  it("shows a failed refresh while the computer runs", async () => {
    sandbox.status.mockResolvedValueOnce(running(false)).mockRejectedValue(new Error("internal: the daemon is not reachable"));
    view();
    fireEvent.click(await screen.findByRole("button", { name: "Take over" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("the daemon is not reachable");
    expect(screen.getByTestId("live-screen")).toBeInTheDocument();
  });
});
