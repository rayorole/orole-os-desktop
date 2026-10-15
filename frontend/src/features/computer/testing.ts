import { vi } from "vitest";

/**
 * The mocked MyGo bridge for the Computer tab tests. Install with
 * `vi.mock("@/mygo", async () => (await import("./testing")).mygoMock)`.
 */
const listeners = new Map<string, Set<(payload: unknown) => void>>();

function event(name: string) {
  return {
    on: (listener: (payload: never) => void) => {
      const set = listeners.get(name) ?? new Set();
      set.add(listener as (payload: unknown) => void);
      listeners.set(name, set);
      return () => { set.delete(listener as (payload: unknown) => void); };
    },
  };
}

/** Delivers a bridge event to every current subscriber. */
export function emit(name: "sandboxStatus" | "sandboxScreen" | "sandboxAvailability" | "sandboxRuntime" | "sandboxSetup" | "sandboxHandoff" | "terminalOutput" | "terminalChanged" | "daemonState", payload: unknown) {
  for (const listener of [...(listeners.get(name) ?? [])]) listener(payload);
}

export const listenerCount = (name: string) => listeners.get(name)?.size ?? 0;

export const sandbox = {
  availability: vi.fn(), status: vi.fn(), enable: vi.fn(), install: vi.fn(), setViewer: vi.fn(), takeOver: vi.fn(),
  release: vi.fn(), handoff: vi.fn(), openScreen: vi.fn(), screenInput: vi.fn(), closeScreen: vi.fn(), openTerminal: vi.fn(),
};
export const terminalsBridge = { attach: vi.fn(), write: vi.fn(), resize: vi.fn(), close: vi.fn() };

/** Clears subscriptions and calls; the void calls resolve by default. */
export function resetSandbox() {
  listeners.clear();
  for (const fn of [...Object.values(sandbox), ...Object.values(terminalsBridge)]) fn.mockReset();
  for (const fn of [sandbox.setViewer, sandbox.takeOver, sandbox.release, sandbox.screenInput, sandbox.closeScreen]) fn.mockResolvedValue(undefined);
  sandbox.enable.mockResolvedValue({ running: false, rebootRequired: false });
  sandbox.install.mockResolvedValue({ ready: false, installing: false, done: 0, total: 0 });
  sandbox.handoff.mockResolvedValue({ botId: "", pending: false });
}

export const mygoMock = {
  Sandbox: sandbox,
  Terminals: terminalsBridge,
  events: {
    sandboxStatus: event("sandboxStatus"), sandboxScreen: event("sandboxScreen"),
    sandboxAvailability: event("sandboxAvailability"), sandboxRuntime: event("sandboxRuntime"),
    sandboxSetup: event("sandboxSetup"), sandboxHandoff: event("sandboxHandoff"),
    terminalOutput: event("terminalOutput"), terminalChanged: event("terminalChanged"), daemonState: event("daemonState"),
  },
};

export const b64 = (bytes: number[]) => btoa(String.fromCharCode(...bytes));
