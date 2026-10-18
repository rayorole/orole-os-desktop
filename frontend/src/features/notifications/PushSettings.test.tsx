import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { NtfyView } from "@/mygo";

const mocks = vi.hoisted(() => ({ ntfy: vi.fn(), setNtfy: vi.fn(), testNtfy: vi.fn() }));
vi.mock("@/mygo", () => ({ Notifications: { ntfy: mocks.ntfy, setNtfy: mocks.setNtfy, testNtfy: mocks.testNtfy } }));
vi.mock("@/stores/daemon", () => ({ useDaemon: (selector: (state: { link: string }) => unknown) => selector({ link: "connected" }) }));
import { PushSettings } from "./PushSettings";

const off: NtfyView = { enabled: false, server: "https://ntfy.sh", topic: "", tokenSet: false };
const on: NtfyView = { enabled: true, server: "https://ntfy.sh", topic: "orole-abc123", tokenSet: true };

beforeEach(() => {
  vi.clearAllMocks();
  mocks.ntfy.mockResolvedValue(off);
});
afterEach(cleanup);

const button = (name: string) => screen.getByRole("button", { name });
const field = (name: string) => screen.getByLabelText(name) as HTMLInputElement;

describe("PushSettings", () => {
  it("starts off, without a topic, and cannot send a test", async () => {
    render(<PushSettings />);
    expect(await screen.findByRole("switch", { name: "Push notifications" })).not.toBeChecked();
    expect(field("Server").value).toBe("https://ntfy.sh");
    expect(button("Send test")).toBeDisabled();
    expect(screen.getByText("The topic works like a password")).toBeVisible();
  });

  it("switching on saves only the master switch and shows the topic the daemon created", async () => {
    mocks.setNtfy.mockResolvedValue(on);
    render(<PushSettings />);
    fireEvent.click(await screen.findByRole("switch", { name: "Push notifications" }));
    await waitFor(() => expect(mocks.setNtfy).toHaveBeenCalledWith({ enabled: true, server: "https://ntfy.sh", token: null, regenerateTopic: false }));
    expect(await screen.findByDisplayValue("orole-abc123")).toBeVisible();
    expect(field("Access token").placeholder).toBe("••••••••••••");
    expect(field("Access token").value).toBe("");
  });

  it("saves a changed server and a new token, then clears the token field", async () => {
    mocks.ntfy.mockResolvedValue(on);
    mocks.setNtfy.mockResolvedValue({ ...on, server: "http://localhost:2586" });
    render(<PushSettings />);
    await screen.findByDisplayValue("orole-abc123");
    expect(button("Save changes")).toBeDisabled();
    fireEvent.change(field("Server"), { target: { value: " http://localhost:2586 " } });
    fireEvent.change(field("Access token"), { target: { value: "tk_new" } });
    expect(button("Send test")).toBeDisabled(); // unsaved edits are not what the test would use
    fireEvent.click(button("Save changes"));
    await waitFor(() => expect(mocks.setNtfy).toHaveBeenCalledWith({ enabled: true, server: "http://localhost:2586", token: "tk_new", regenerateTopic: false }));
    await waitFor(() => expect(field("Access token").value).toBe(""));
    expect(field("Server").value).toBe("http://localhost:2586");
  });

  it("removes a stored token without touching the rest", async () => {
    mocks.ntfy.mockResolvedValue(on);
    mocks.setNtfy.mockResolvedValue({ ...on, tokenSet: false });
    render(<PushSettings />);
    await screen.findByDisplayValue("orole-abc123");
    fireEvent.click(button("Remove"));
    await waitFor(() => expect(mocks.setNtfy).toHaveBeenCalledWith({ enabled: true, server: "https://ntfy.sh", token: "", regenerateTopic: false }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Remove" })).toBeNull());
  });

  it("reports a successful and a failed test push", async () => {
    mocks.ntfy.mockResolvedValue(on);
    mocks.testNtfy.mockResolvedValueOnce({ ok: true }).mockResolvedValueOnce({ ok: false, error: "ntfy answered 403 Forbidden: no access to this topic" });
    render(<PushSettings />);
    await screen.findByDisplayValue("orole-abc123");
    fireEvent.click(button("Send test"));
    expect(await screen.findByText(/^Sent\./)).toBeVisible();
    fireEvent.click(button("Send test"));
    expect(await screen.findByText(/403 Forbidden/)).toBeVisible();
  });

  it("regenerates the topic only after confirmation", async () => {
    mocks.ntfy.mockResolvedValue(on);
    mocks.setNtfy.mockResolvedValue({ ...on, topic: "orole-new456" });
    render(<PushSettings />);
    await screen.findByDisplayValue("orole-abc123");
    fireEvent.click(button("Regenerate"));
    expect(mocks.setNtfy).not.toHaveBeenCalled();
    fireEvent.click(await within(await screen.findByRole("alertdialog")).findByRole("button", { name: "Regenerate" }));
    await waitFor(() => expect(mocks.setNtfy).toHaveBeenCalledWith({ enabled: true, server: "https://ntfy.sh", token: null, regenerateTopic: true }));
    expect(await screen.findByDisplayValue("orole-new456")).toBeVisible();
  });

  it("shows why the settings could not be loaded", async () => {
    mocks.ntfy.mockRejectedValue(new Error("daemon unavailable"));
    render(<PushSettings />);
    expect(await screen.findByText("daemon unavailable")).toBeVisible();
  });
});
