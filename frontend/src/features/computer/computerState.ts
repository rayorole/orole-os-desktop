import type { Sandbox } from "@/mygo";

export type Availability = Awaited<ReturnType<typeof Sandbox.availability>>;
export type ComputerStatus = Awaited<ReturnType<typeof Sandbox.status>>;
export type Handoff = Awaited<ReturnType<typeof Sandbox.handoff>>;
export type RuntimeInfo = Awaited<ReturnType<typeof Sandbox.install>>;
export type SetupInfo = Awaited<ReturnType<typeof Sandbox.enable>>;

export type ComputerView =
  | { kind: "loading" }
  | { kind: "setup"; reason: string; detail: string }
  | { kind: "off" }
  | { kind: "starting" }
  | { kind: "stopping" }
  | { kind: "failed"; error: string }
  | {
      kind: "running";
      takenOver: boolean;
      /** Set while the bot waits for the user to take over (sandbox:handoff pending) and the user has not yet. */
      waitingForUser?: boolean;
      /** Why the bot asked for the user, when it said. */
      handoffReason?: string;
    };

/** What the Computer tab shows for the PC's availability, the bot's computer status and its handoff request. */
export function computerView(
  availability: Availability | null,
  status: ComputerStatus | null,
  handoff?: Handoff | null,
): ComputerView {
  if (!availability || !status) return { kind: "loading" };
  if (!availability.ready) return { kind: "setup", reason: availability.reason ?? "", detail: availability.detail ?? "" };
  switch (status.state) {
    case "starting": return { kind: "starting" };
    case "running": {
      const view: ComputerView = { kind: "running", takenOver: status.takenOver };
      if (handoff?.pending) {
        if (handoff.reason) view.handoffReason = handoff.reason;
        if (!status.takenOver) view.waitingForUser = true;
      }
      return view;
    }
    case "stopping": return { kind: "stopping" };
    case "failed": return { kind: "failed", error: status.error || "The computer failed to start." };
    default: return { kind: "off" };
  }
}

export type SetupAction = "enable" | "install" | "recheck" | null;

/**
 * The setup screen's copy for an Availability reason. The body is the daemon's own detail text (it knows the host and
 * the download size), so nothing about Windows versions or firmware is hardcoded here; the title and the action
 * button depend on the reason only.
 */
export function setupCopy(reason: string, detail = ""): { title: string; body: string; action: SetupAction; actionLabel: string | null } {
  const body = (fallback: string) => detail.trim() || fallback;
  switch (reason) {
    case "whp-disabled":
      return { title: "Turn on Windows Hypervisor Platform", body: body("Windows Hypervisor Platform is turned off."), action: "enable", actionLabel: "Enable" };
    case "runtime-missing":
      return { title: "Download the bot computer", body: body("The virtual machine software and the Linux image need to be downloaded."), action: "install", actionLabel: "Download" };
    case "reboot-required":
      return { title: "Restart Windows to finish", body: body("Windows needs a restart to finish turning on virtualization."), action: "recheck", actionLabel: "Check again" };
    case "virtualization-off":
      return { title: "Virtualization is not available", body: body("Orole could not use virtualization on this PC."), action: null, actionLabel: null };
    case "unsupported-os":
      return { title: "Not available on this PC", body: body("This version of Windows can't run a bot computer."), action: null, actionLabel: null };
    default:
      return { title: "The computer can't run here", body: body("Orole could not use virtualization on this PC."), action: "recheck", actionLabel: "Check again" };
  }
}

/** Shown in the Computer pane's footer: what a bot computer can reach and what it keeps. */
export const SECURITY_NOTE = "The computer can reach the internet, your home network and anything on this PC that accepts network connections. Sites you sign into stay signed in, and the bot can use them.";

const STEP_LABELS: Record<string, string> = { qemu: "Virtual machine software", image: "Linux system image" };

/**
 * Download progress. The daemon reports done/total per file and they reset for each file, and the pinned total size is
 * not exposed to the UI, so this shows the current step's name and the percent of that file (null until its size is
 * known). It never claims an overall percentage.
 */
export function runtimeProgress(r: RuntimeInfo | null): { label: string; percent: number | null } | null {
  if (!r?.installing) return null;
  const label = r.step ? (STEP_LABELS[r.step] ?? r.step) : "Starting download";
  const percent = r.total > 0 ? Math.min(100, Math.max(0, Math.floor((r.done / r.total) * 100))) : null;
  return { label, percent };
}

/** The error to show on the setup screen from the install/enable events, if any. */
export function setupError(runtime: RuntimeInfo | null, setup: SetupInfo | null): string | null {
  return runtime?.error || setup?.error || null;
}

/** An error for display, without the daemon's code prefix ("not_found: …"). */
export const errorText = (e: unknown): string => (e instanceof Error ? e.message : String(e)).replace(/^[a-z_]+: /, "");
