import { useRef, useState } from "react";
import { Agents, type HarnessOption, type ModelOption, type Session } from "@/mygo";
import { inDesktop } from "@/lib/backend";
import { useAgents } from "@/stores/agents";
import { errText } from "@/features/agents/format";
import { useHarnessOptions } from "@/features/agents/useHarnessOptions";
import { MAX_FILES, useTaskAttachments, type TaskAttachment } from "@/features/home/useTaskAttachments";
import { composerState } from "./composerState";

export interface TaskMessageAttachments {
  files: TaskAttachment[];
  ids: string[];
  ready: boolean;
  canAdd: boolean;
  add: (files: File[]) => Promise<void>;
  remove: (key: string) => void;
  clear: () => void;
}

export interface TaskMessage {
  session: Session;
  harness: HarnessOption | undefined;
  models: ModelOption[];
  model: string;
  reasoning: string;
  levels: string[];
  busy: boolean;
  error: string | null;
  attachments: TaskMessageAttachments;
  canChooseModel: boolean;
  canChooseReasoning: boolean;
  settingsReason: string;
  attachmentReason: string;
  modelBlockedReason: (option: ModelOption) => string;
  chooseModel: (value: string) => void;
  chooseReasoning: (value: string) => void;
  send: (text: string) => Promise<void>;
}

/** Message draft controls for the session-keyed composer; the daemon owns delivery and queuing. */
export function useTaskMessage(session: Session): TaskMessage {
  const source = useHarnessOptions(session.machine || "local");
  const harness = source.options?.harnesses.find((option) => option.id === session.kind);
  const [selection, setSelection] = useState<{ model: string; reasoning: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inFlight = useRef(false);
  const state = composerState(session);
  const connected = inDesktop && !source.offline;
  const capabilities = session.followup;
  const cannotAttach = !connected ? "Connect to the desktop daemon to attach files."
    : !state.canSend ? state.reason
    : !capabilities?.attachments ? capabilities?.reason || "Attachments are unavailable for this conversation."
    : "";
  const attachments = useTaskAttachments(!cannotAttach && !busy, [], cannotAttach);
  const models = harness?.models ?? [];
  const model = capabilities?.model ? selection?.model ?? session.model : session.model;
  const reasoning = capabilities?.reasoning ? selection?.reasoning ?? session.reasoning ?? "" : session.reasoning ?? "";
  const modelOption = models.find((option) => option.id === model);
  const levels = modelOption ? modelOption.reasoning : harness?.reasoning ?? [];
  const controlsBlocked = !connected ? "Connect to the desktop daemon to change message settings."
    : !state.canSend ? state.reason
    : source.loading ? "Discovering models on this system…"
    : source.error || source.options?.unavailableReason || (!harness ? "This conversation's agent is not available on this system." : !harness.available ? harness.unavailableReason || "This conversation's agent is unavailable." : "");
  const canChooseModel = !busy && !controlsBlocked && !!capabilities?.model && models.length > 0;
  const canChooseReasoning = !busy && !controlsBlocked && !!capabilities?.reasoning && levels.length > 0;
  const settingsReason = controlsBlocked || ((!capabilities?.model || !capabilities?.reasoning) ? capabilities?.reason || "This conversation does not support changing these settings." : "");
  const attachmentReason = cannotAttach || (attachments.files.length >= MAX_FILES ? `Attach up to ${MAX_FILES} files per message.` : "");

  function modelBlockedReason(option: ModelOption) {
    if (option.id === session.model) return "";
    return reasoning && !option.reasoning.includes(reasoning) && (!capabilities?.reasoning || !option.reasoning.length)
      ? "This model cannot preserve the conversation's reasoning effort."
      : "";
  }

  function chooseModel(value: string) {
    const next = models.find((option) => option.id === value);
    if (!canChooseModel || inFlight.current || !next || modelBlockedReason(next)) return;
    const nextReasoning = reasoning && !next.reasoning.includes(reasoning)
      ? next.id === session.model ? session.reasoning ?? "" : next.reasoning[0] ?? reasoning
      : reasoning;
    setSelection({ model: value, reasoning: nextReasoning });
  }

  function chooseReasoning(value: string) {
    const original = model === session.model && value === (session.reasoning ?? "");
    if (!canChooseReasoning || inFlight.current || (!levels.includes(value) && !original && !(value === "" && !session.reasoning))) return;
    setSelection({ model, reasoning: value });
  }

  async function send(text: string): Promise<void> {
    try {
      if (inFlight.current) throw new Error("A message is already being sent.");
      if (!state.canSend) throw new Error(state.reason);
      if (!connected) throw new Error("Connect to the desktop daemon to send a message.");
      if (!attachments.ready) throw new Error("Wait for attachments to upload, or remove failed uploads before sending.");
      if (!text.trim() && attachments.ids.length === 0) throw new Error("Write a message or attach a file before sending.");
      if (attachments.ids.length > 0 && !capabilities?.attachments) throw new Error(attachmentReason);
    } catch (cause) {
      setError(errText(cause));
      throw cause;
    }
    inFlight.current = true;
    setBusy(true);
    setError(null);
    try {
      const updated = session.mode !== "structured"
        ? await Agents.prompt(session.id, text.trim())
        : await Agents.sendMessage(
        session.id,
        text.trim(),
        attachments.ids,
        capabilities?.model ? model : "",
        capabilities?.reasoning ? reasoning : "",
      );
      useAgents.getState().upsert(updated);
      attachments.clear();
      setSelection(null);
    } catch (cause) {
      setError(errText(cause));
      throw cause;
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }

  return { session, harness, models, model, reasoning, levels, busy, error, attachments, canChooseModel, canChooseReasoning, settingsReason, attachmentReason, modelBlockedReason, chooseModel, chooseReasoning, send };
}
