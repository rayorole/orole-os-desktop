import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Prefs } from "@/mygo";

const mocks = vi.hoisted(() => ({ link: "online", refresh: vi.fn(), startSync: vi.fn(), setKindPref: vi.fn(), loadPrefs: vi.fn(), playSound: vi.fn() }));
vi.mock("@/lib/backend", () => ({ inDesktop: true }));
vi.mock("@/lib/audio", () => ({ playSound: mocks.playSound }));
vi.mock("@/stores/daemon", () => ({ useDaemon: Object.assign((selector: (state: { link: string }) => unknown) => selector({ link: mocks.link }), { getState: () => ({ refresh: mocks.refresh }) }) }));
vi.mock("@/stores/notifications", () => ({ useNotifications: vi.fn() }));
vi.mock("./PushSettings", () => ({ PushSettings: () => null }));
import { useNotifications } from "@/stores/notifications";
import { NotificationSettings } from "./NotificationSettings";

function notificationState() {
  const prefs: Prefs = { kinds: {
    approval: { inApp: true, sound: true, push: false }, failed: { inApp: true, sound: true, push: false }, finished: { inApp: true, sound: false, push: false },
    reply: { inApp: false, sound: true, push: false }, message: { inApp: true, sound: true, push: false },
  } };
  return {
    prefs, prefsLoading: false, prefsSaving: false, prefsError: null as string | null,
    startSync: mocks.startSync, setKindPref: mocks.setKindPref, loadPrefs: mocks.loadPrefs,
  };
}
let state = notificationState();
beforeEach(() => {
  vi.clearAllMocks();
  mocks.link = "online";
  state = notificationState();
  vi.mocked(useNotifications).mockImplementation(() => state);
});
afterEach(cleanup);

const button = (name: string) => screen.getByRole("button", { name });

describe("NotificationSettings", () => {
  it("shows each kind's stored channels", () => {
    render(<NotificationSettings />);
    expect(button("Play a sound for Finished")).toHaveAttribute("aria-pressed", "false");
    expect(button("Show Bot replies in app")).toHaveAttribute("aria-pressed", "false");
    // Hidden kinds make no sound, whatever was stored.
    expect(button("Play a sound for Bot replies")).toHaveAttribute("aria-pressed", "false");
    expect(button("Play a sound for Bot replies")).toBeDisabled();
    expect(button("Show Failed in app")).toHaveAttribute("aria-pressed", "true");
  });

  it("saves one kind at a time and previews a sound being turned on", () => {
    render(<NotificationSettings />);
    fireEvent.click(button("Play a sound for Finished"));
    expect(mocks.setKindPref).toHaveBeenLastCalledWith("finished", { inApp: true, sound: true, push: false });
    expect(mocks.playSound).toHaveBeenLastCalledWith("finished", "notification");
    fireEvent.click(button("Show Failed in app"));
    expect(mocks.setKindPref).toHaveBeenLastCalledWith("failed", { inApp: false, sound: true, push: false });
    fireEvent.click(button("Play the Failed sound"));
    expect(mocks.playSound).toHaveBeenLastCalledWith("failed", "notification");
  });

  it("never lets approvals be hidden", () => {
    render(<NotificationSettings />);
    expect(button("Show Needs you in app")).toBeDisabled();
    expect(button("Play a sound for Needs you")).toBeEnabled();
  });

  it("toggles push per kind, off unless stored, and independent of the other channels", () => {
    state.prefs!.kinds.failed = { inApp: true, sound: true, push: true };
    state.prefs!.kinds.reply = { inApp: false, sound: false, push: true };
    render(<NotificationSettings />);
    expect(button("Send Failed as a push notification")).toHaveAttribute("aria-pressed", "true");
    expect(button("Send Finished as a push notification")).toHaveAttribute("aria-pressed", "false");
    // A kind hidden in app can still be pushed.
    expect(button("Send Bot replies as a push notification")).toBeEnabled();
    fireEvent.click(button("Send Finished as a push notification"));
    expect(mocks.setKindPref).toHaveBeenLastCalledWith("finished", { inApp: true, sound: false, push: true });
    fireEvent.click(button("Send Failed as a push notification"));
    expect(mocks.setKindPref).toHaveBeenLastCalledWith("failed", { inApp: true, sound: true, push: false });
    // Approvals stay shown but are pushed only when asked.
    fireEvent.click(button("Send Needs you as a push notification"));
    expect(mocks.setKindPref).toHaveBeenLastCalledWith("approval", { inApp: true, sound: true, push: true });
  });

  it.each(["offline", "loading", "saving"])("disables changes while %s", (mode) => {
    mocks.link = mode === "offline" ? "offline" : "online";
    state.prefsLoading = mode === "loading";
    state.prefsSaving = mode === "saving";
    render(<NotificationSettings />);
    for (const kind of ["Failed", "Finished", "Flow messages"]) expect(button(`Show ${kind} in app`)).toBeDisabled();
  });

  it("keeps errors and connection recovery visible", () => {
    state.prefsError = "Could not save preferences";
    render(<NotificationSettings />);
    expect(screen.getByText("Could not save preferences")).toBeVisible();
    fireEvent.click(button("Retry connection"));
    expect(mocks.refresh).toHaveBeenCalledTimes(1);
    expect(mocks.loadPrefs).toHaveBeenCalledTimes(1);
  });
});
