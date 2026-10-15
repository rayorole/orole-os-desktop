import { describe, expect, it, vi } from "vitest";

vi.mock("@/mygo", async () => (await import("./testing")).mygoMock);

import { computerView, errorText, runtimeProgress, SECURITY_NOTE, setupCopy, setupError, type Availability, type ComputerStatus } from "./computerState";

const ready: Availability = { ready: true };
const status = (over: Partial<ComputerStatus>): ComputerStatus => ({ botId: "b1", state: "off", takenOver: false, ...over }) as ComputerStatus;

describe("computer view", () => {
  it.each([
    [null, null, { kind: "loading" }],
    [{ ready: false, reason: "whp-disabled", detail: "HypervisorPlatform: Disabled" }, status({}), { kind: "setup", reason: "whp-disabled", detail: "HypervisorPlatform: Disabled" }],
    [ready, status({ state: "off" }), { kind: "off" }],
    [ready, status({ state: "starting" }), { kind: "starting" }],
    [ready, status({ state: "stopping" }), { kind: "stopping" }],
    [ready, status({ state: "failed", error: "the computer failed to start" }), { kind: "failed", error: "the computer failed to start" }],
    [ready, status({ state: "failed" }), { kind: "failed", error: "The computer failed to start." }],
    [ready, status({ state: "running" }), { kind: "running", takenOver: false }],
    [ready, status({ state: "running", takenOver: true }), { kind: "running", takenOver: true }],
  ] as const)("maps %j + %j", (a, s, want) => {
    expect(computerView(a as Availability | null, s as ComputerStatus | null)).toEqual(want);
  });

  it("carries a pending handoff while running", () => {
    const h = { botId: "b1", pending: true, reason: "sign in" };
    expect(computerView(ready, status({ state: "running" }), h)).toEqual({ kind: "running", takenOver: false, waitingForUser: true, handoffReason: "sign in" });
    expect(computerView(ready, status({ state: "running", takenOver: true }), h)).toEqual({ kind: "running", takenOver: true, handoffReason: "sign in" });
    expect(computerView(ready, status({ state: "running" }), { botId: "b1", pending: false })).toEqual({ kind: "running", takenOver: false });
    expect(computerView(ready, status({ state: "off" }), h)).toEqual({ kind: "off" });
  });
});

describe("setup copy", () => {
  it("offers the right action and button for each reason", () => {
    expect(setupCopy("whp-disabled")).toMatchObject({ action: "enable", actionLabel: "Enable" });
    expect(setupCopy("runtime-missing")).toMatchObject({ action: "install", actionLabel: "Download" });
    expect(setupCopy("reboot-required")).toMatchObject({ title: "Restart Windows to finish", action: "recheck" });
    expect(setupCopy("virtualization-off")).toMatchObject({ action: null, actionLabel: null });
    expect(setupCopy("unsupported-os")).toMatchObject({ action: null, actionLabel: null });
    expect(setupCopy("something-new").action).toBe("recheck");
  });

  it("shows the daemon's detail as the body and hardcodes no OS claims", () => {
    const detail = "Download the computer (QEMU and the Linux image, about 1.6 GB) to continue.";
    expect(setupCopy("runtime-missing", detail).body).toBe(detail);
    for (const r of ["whp-disabled", "runtime-missing", "reboot-required", "virtualization-off", "unsupported-os", "x"]) {
      expect(setupCopy(r).body + setupCopy(r).title).not.toMatch(/Windows 11|BIOS/);
    }
  });
});

describe("download progress", () => {
  it("is null unless installing", () => {
    expect(runtimeProgress(null)).toBeNull();
    expect(runtimeProgress({ ready: true, installing: false, done: 5, total: 10 })).toBeNull();
  });

  it("names the step and the percent of the current file", () => {
    expect(runtimeProgress({ ready: false, installing: true, step: "image", done: 250, total: 1000 })).toEqual({ label: "Linux system image", percent: 25 });
    expect(runtimeProgress({ ready: false, installing: true, step: "qemu", done: 0, total: 0 })).toEqual({ label: "Virtual machine software", percent: null });
    expect(runtimeProgress({ ready: false, installing: true, done: 0, total: 0 })).toEqual({ label: "Starting download", percent: null });
  });

  it("surfaces errors from either event", () => {
    expect(setupError({ ready: false, installing: false, done: 0, total: 0, error: "checksum mismatch" }, null)).toBe("checksum mismatch");
    expect(setupError(null, { running: false, rebootRequired: false, error: "UAC declined" })).toBe("UAC declined");
    expect(setupError(null, null)).toBeNull();
  });
});

describe("errorText", () => {
  it("strips the daemon's error code from messages", () => {
    expect(errorText(new Error("not_found: the computer is off"))).toBe("the computer is off");
    expect(errorText("plain")).toBe("plain");
  });

  it("says plainly what the computer can reach and what it keeps", () => {
    expect(SECURITY_NOTE).toBe("The computer can reach the internet, your home network and anything on this PC that accepts network connections. Sites you sign into stay signed in, and the bot can use them.");
  });
});
