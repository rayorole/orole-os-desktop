import { useEffect, useRef, useState } from "react";
import { Settings, type Mode, type StartSpec } from "@/mygo";
import { inDesktop } from "@/lib/backend";
import { useAgents } from "@/stores/agents";
import { useBoxes } from "@/stores/boxes";
import { useProjects } from "@/stores/projects";
import { useHarnessOptions } from "@/features/agents/useHarnessOptions";
import { newKey } from "@/features/agents/format";
import { toastError } from "@/features/projects/dialogs";
import { beginLaunch } from "@/features/tasks/pendingLaunch";
import { modeSupport, type PermissionMode } from "@/features/tasks/permissionModes";
import { useTaskAttachments } from "./useTaskAttachments";
import { loadProviderPreferences, saveProviderPreferences } from "./provider-preferences";

export interface TaskLaunchOptions {
  /** Which launcher this is, so a failed launch returns to the right one. */
  launcher?: "home" | "new-task";
  initialProjectId?: string | null;
  initialPrompt?: string;
  onStarted?: () => void;
}

const PERMISSION_KEY = "orole.task.permissionMode";

/** The locally remembered choice, used only while the daemon has no default. YOLO is never remembered here: it must be chosen, and confirmed, for each launch. */
function savedPermissionMode(): PermissionMode {
  try { return localStorage.getItem(PERMISSION_KEY) === "auto" ? "auto" : "manual"; } catch { return "manual"; }
}

/** The daemon's `agents.defaultPermissionMode`, or null when unset (or unreachable). Yolo as the default counts as confirmed: it was chosen in Settings. */
async function daemonDefaultMode(): Promise<PermissionMode | null> {
  if (!inDesktop) return null;
  try {
    const mode = ((await Settings.all()).agents as { defaultPermissionMode?: unknown } | undefined)?.defaultPermissionMode;
    return mode === "auto" || mode === "yolo" || mode === "manual" ? mode : null;
  } catch { return null; }
}

/** Stores the choice as the daemon's `agents.defaultPermissionMode`, which new tasks (the user's and bots') start in. Read fresh and written back whole, so keys other controls own survive. Running tasks keep their mode. */
async function saveDefaultMode(mode: PermissionMode): Promise<void> {
  if (!inDesktop) return;
  const current = ((await Settings.all()).agents as Record<string, unknown> | undefined) ?? {};
  if (current.defaultPermissionMode === mode) return;
  await Settings.set("agents", { ...current, defaultPermissionMode: mode });
}

export function useHomeTaskLaunch({ launcher = "home", initialProjectId, initialPrompt = "", onStarted }: TaskLaunchOptions = {}) {
  // A failed launch sent back by "Edit prompt" for this kind of launcher: every choice starts as it was.
  const [draft] = useState(() => { const held = useAgents.getState().launchDraft; return held?.launcher === launcher ? held : null; });
  useEffect(() => { if (draft && useAgents.getState().launchDraft === draft) useAgents.getState().setLaunchDraft(null); }, [draft]);
  const projects = useProjects((s) => s.projects);
  const selectedId = useProjects((s) => s.selectedId);
  const worktrees = useProjects((s) => s.worktrees);
  const loaded = useProjects((s) => s.loaded);
  const machines = useBoxes((s) => s.machines);
  const observations = useBoxes((s) => s.observations);
  const now = useBoxes((s) => s.now);
  const [machine, setMachine] = useState(() => draft?.machine ?? projects.find((p) => p.id === initialProjectId)?.machineId ?? "local");
  const source = useHarnessOptions(machine);
  const [savedProject] = useState(() => localStorage.getItem("orole.home.project"));
  const [remoteProject, setRemoteProject] = useState(draft?.project ?? initialProjectId ?? "");
  const registered = projects.filter((p) => p.machineId === machine).map((p) => ({ ...p, registered: true }));
  const choices = [...registered, ...(source.options?.projects ?? []).filter((p) => p.machine === machine && !registered.some((r) => r.id === p.id || r.path === p.path)).map((p) => ({ id: p.id, name: p.name, path: p.path, machineId: p.machine, isGit: p.git, missing: false, registered: false }))];
  const project = choices.find((p) => p.id === (remoteProject || selectedId || savedProject)) ?? choices.find((p) => !p.missing);
  const [worktreeChoice, setWorktreeChoice] = useState<{ project: string; value: string } | null>(draft?.worktree ?? null);
  const canIsolate = Boolean(project?.isGit && source.options?.worktree);
  const needsTrees = Boolean(project?.isGit);
  // Worktrees cost a git call per project, so they are fetched only while the picked project can use them.
  useEffect(() => needsTrees ? useProjects.getState().watchWorktrees() : undefined, [needsTrees]);
  const trees = project ? worktrees[project.id] ?? [] : [];
  const preferredTree = worktreeChoice?.project === project?.id ? worktreeChoice?.value : undefined;
  const tree = preferredTree && trees.find((w) => w.id === preferredTree && !w.error);
  // The current checkout unless a new worktree (or an existing one) was picked for this project.
  const worktree = tree ? tree.id : preferredTree === "new" && canIsolate ? "new" : "current";
  const [kind, setKind] = useState(() => draft?.harness ?? localStorage.getItem("orole.task.harness") ?? "");
  const harnesses = (source.options?.harnesses ?? []).filter((h) => machine === "local" ? h.structured && h.modes.includes("structured") : h.modes.includes("process"));
  const harness = harnesses.find((h) => h.id === kind && h.available) ?? harnesses.find((h) => h.available);
  const [preferences, setPreferences] = useState(loadProviderPreferences);
  const selected = (harness && preferences[harness.id]) || { model: "", reasoning: "" };
  const models = harness?.models ?? [];
  const model = models.some((m) => m.id === selected.model) ? selected.model : models.find((m) => m.default)?.id || models[0]?.id || "";
  const modelOption = models.find((m) => m.id === model);
  const reasoningLevels = modelOption ? modelOption.reasoning : harness?.reasoning ?? [];
  const reasoning = reasoningLevels.includes(selected.reasoning) ? selected.reasoning : "";
  const [chosenMode, setChosenMode] = useState<PermissionMode>(() => draft?.permissionMode ?? savedPermissionMode());
  const [yoloConfirmed, setYoloConfirmed] = useState(false);
  // The daemon's default wins over the local memory once it arrives, unless a restored draft or the user already picked a mode.
  const touched = useRef(draft !== null);
  const daemonDefault = useRef<PermissionMode | null>(null);
  useEffect(() => {
    let live = true;
    void daemonDefaultMode().then((mode) => {
      if (!live || !mode) return;
      daemonDefault.current = mode;
      if (touched.current) return;
      setChosenMode(mode);
      setYoloConfirmed(mode === "yolo");
    });
    return () => { live = false; };
  }, []);
  // A mode this agent cannot run (or that was turned off) falls back to Manual rather than launching in it.
  const permissionSupport = harness?.permissionModes ?? [];
  const permissionMode: PermissionMode = modeSupport(permissionSupport, chosenMode).available && (chosenMode !== "yolo" || yoloConfirmed) ? chosenMode : "manual";
  const [prompt, setPrompt] = useState(draft?.prompt ?? initialPrompt);
  const [busy, setBusy] = useState(false);
  const attachments = useTaskAttachments(inDesktop && !busy && !source.offline, draft?.attachments, !inDesktop ? "Attaching files needs the desktop app." : source.offline ? "Connect to the desktop daemon to attach files." : "");
  const inFlight = useRef(false);
  const attempt = useRef<{ signature: string; key: string } | null>(draft?.attempt ?? null);
  const blocked = !inDesktop ? "Open the desktop app to start a task."
    : source.offline ? "The daemon is offline."
    : !loaded ? "Loading projects…"
    : source.loading ? "Discovering agents on this system…"
    : source.error || source.options?.unavailableReason || (!project ? "Select or create a project on this system." : project.missing ? "This project's folder is missing." : !harness ? "No supported agent is installed on this system." : "");
  const canStart = !blocked && (Boolean(prompt.trim()) || attachments.ids.length > 0) && attachments.ready && !busy;
  function chooseProject(id: string) {
    const p = choices.find((p) => p.id === id);
    if (!p) return;
    setRemoteProject(id);
    if (p.registered) useProjects.getState().select(id);
    localStorage.setItem("orole.home.project", id);
    setWorktreeChoice(null);
  }
  function choosePermissionMode(mode: PermissionMode, confirmYolo: boolean) {
    touched.current = true;
    setChosenMode(mode);
    setYoloConfirmed(mode === "yolo" && confirmYolo);
    saveDefaultMode(mode).then(() => { daemonDefault.current = mode; }, (error: unknown) => toastError("Could not save the permission mode", error));
    if (mode === "yolo") return;
    try { localStorage.setItem(PERMISSION_KEY, mode); } catch (error) { toastError("Could not save the permission mode", error); }
  }
  function chooseMachine(id: string) {
    setMachine(id); setRemoteProject(""); setWorktreeChoice(null);
  }
  function chooseAgent(id: string, nextModel?: string, nextReasoning?: string) {
    setKind(id);
    try {
      localStorage.setItem("orole.task.harness", id);
      if (nextModel === undefined && nextReasoning === undefined) return;
      const previous = preferences[id] ?? { model: "", reasoning: "" };
      const next = { ...preferences, [id]: { model: nextModel ?? previous.model, reasoning: nextReasoning ?? previous.reasoning } };
      setPreferences(next);
      saveProviderPreferences(next);
    } catch (error) { toastError("Could not save agent preferences", error); }
  }
  /**
   * Hands the task to the pending task page, which shows at once and runs `Agents.start` behind it. The prompt and uploads
   * leave this form with it; "Edit prompt" on a failure restores them, with the same idempotency key for an identical resend.
   */
  function start() {
    if (!canStart || !project || !harness || inFlight.current) return;
    const spec: Omit<StartSpec, "key"> = {
      kind: harness.id, machine, projectId: project.registered ? project.id : "", dir: tree ? tree.path : project.registered ? "" : project.path,
      prompt: prompt.trim(), model, reasoning, mode: (machine === "local" ? "structured" : "process") as Mode,
      attachmentIds: attachments.ids,
      newWorktree: worktree === "new", branch: "", baseRef: "", permissionMode, confirmYolo: permissionMode === "yolo", command: "", title: "", parentId: "", group: "",
    };
    const signature = JSON.stringify(spec);
    if (attempt.current?.signature !== signature) attempt.current = { signature, key: newKey() };
    const key = attempt.current.key;
    const harnessId = harness.id;
    const projectId = project.id;
    inFlight.current = true; setBusy(true);
    const files = attachments.release();
    const fallback = daemonDefault.current ?? savedPermissionMode();
    setPrompt(""); setChosenMode(fallback); setYoloConfirmed(fallback === "yolo");
    beginLaunch({
      request: { ...spec, key }, prompt: spec.prompt, attachments: files, projectId: project.registered ? project.id : "",
      draft: {
        launcher, initialProjectId: initialProjectId ?? null, returnTo: useProjects.getState().newTask.from, prompt, attachments: files, machine, project: projectId,
        worktree: worktreeChoice, harness: harnessId, permissionMode: permissionMode === "yolo" ? "manual" : permissionMode, attempt: { signature, key },
      },
      onSettled: (session) => {
        inFlight.current = false; setBusy(false);
        if (!session) return;
        if (attempt.current?.key === key) attempt.current = null;
        try {
          localStorage.setItem("orole.task.harness", harnessId);
          localStorage.setItem("orole.home.project", projectId);
        } catch { /* Remembering the choice is a convenience; the task has started either way. */ }
        onStarted?.();
      },
    });
  }
  return { machine, machines, observations, now, choices, project, trees, worktree, canIsolate, harnesses, harness, model, reasoning, prompt, setPrompt, busy, blocked, canStart, chooseProject, chooseMachine, chooseAgent, start, attachments, permissionMode, permissionSupport, choosePermissionMode,
    chooseWorktree: (value: string) => { if (project) setWorktreeChoice({ project: project.id, value }); },
    createProject: () => { chooseMachine("local"); useProjects.getState().setNewProjectOpen(true); },
  };
}

export type HomeTaskLaunch = ReturnType<typeof useHomeTaskLaunch>;
