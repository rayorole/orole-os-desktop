import { useEffect, useRef, useState } from "react";
import { MicIcon, SquareIcon, XIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Popover, PopoverPopup, PopoverTitle, PopoverTrigger } from "@/components/ui/popover";
import { errText } from "@/features/agents/format";
import { inDesktop } from "@/lib/backend";
import { Dictation, type ModelStatus, type VoiceStatus } from "@/mygo";
import { dictationError, megabytes, useDictation } from "@/stores/dictation";
import { useUI } from "@/stores/ui";

type VoiceState = "idle" | "starting" | "recording" | "transcribing";
type Readiness =
  | { kind: "checking" }
  | { kind: "unavailable"; message: string }
  | { kind: "unsupported" }
  | { kind: "no-model"; model: ModelStatus | undefined }
  | { kind: "downloading"; model: ModelStatus }
  | { kind: "ready"; minutes: number };

/** Mirrors the daemon's limits until `Dictation.status` reports them; the daemon enforces them either way. */
const DEFAULT_LIMITS = { maxSeconds: 120, sampleRate: 16_000, partBytes: 1 << 20 };
/** Bounds the compressed recording held in memory. Speech-grade Opus is about 4 KB per second, so two minutes stay far below it. */
const MAX_RECORDING_BYTES = 2 << 20;
const AUDIO_BITS_PER_SECOND = 32_000;
const SLICE_MS = 1000;
const MIME_CANDIDATES = ["audio/webm;codecs=opus", "audio/webm", "audio/ogg;codecs=opus", "audio/mp4"];
const NO_AUDIO = "No audio was captured. Check that your microphone is working and try again.";

interface Run {
  stream?: MediaStream;
  recorder?: MediaRecorder;
  chunks: Blob[];
  bytes: number;
  startedAt: number;
  timer?: number;
  /** Set when the user cancels or the component goes away: late recorder events must do nothing. */
  discarded: boolean;
  limitNotice: string;
  /** The daemon's name for this recording, once its upload has started. */
  id?: string;
}

const pickMime = () => typeof MediaRecorder.isTypeSupported === "function" ? MIME_CANDIDATES.find((type) => MediaRecorder.isTypeSupported(type)) : undefined;

/** Stops the timer, the recorder and every microphone track. Safe to call twice. */
function release(run: Run) {
  window.clearInterval(run.timer);
  run.timer = undefined;
  const recorder = run.recorder;
  if (recorder) {
    recorder.ondataavailable = recorder.onerror = recorder.onstop = null;
    if (recorder.state !== "inactive") {
      try { recorder.stop(); } catch { /* already stopped */ }
    }
  }
  run.stream?.getTracks().forEach((track) => track.stop());
}

/** Drops an uploaded recording in the daemon. Best effort: an unfinished upload expires there on its own. */
function discard(id: string | undefined) {
  if (id) Dictation.discard({ id }).catch(() => {});
}

function micError(failure: unknown): string {
  const name = typeof failure === "object" && failure !== null && "name" in failure ? String(failure.name) : "";
  switch (name) {
    case "NotAllowedError":
    case "SecurityError":
      return "Microphone access was denied. Allow microphone access for Orole OS in Windows Settings > Privacy & security > Microphone, then try again, or type your message.";
    case "NotFoundError":
    case "OverconstrainedError":
    case "DevicesNotFoundError":
      return "No microphone was found. Connect a microphone and try again, or type your message.";
    case "NotReadableError":
    case "AbortError":
    case "TrackStartError":
      return "The microphone could not be opened. Another app may be using it. Close that app and try again.";
    default:
      return `Could not start recording: ${errText(failure)}. You can still type your message.`;
  }
}

/**
 * Decodes the recorded container and renders it as mono at `rate`, the format the speech engine reads, as signed
 * 16-bit little-endian PCM. Rendering one channel downmixes the recording's channels.
 */
async function decodePCM(blob: Blob, rate: number): Promise<Uint8Array> {
  const context = new AudioContext();
  let decoded: AudioBuffer;
  try {
    decoded = await context.decodeAudioData(await blob.arrayBuffer());
  } finally {
    void context.close().catch(() => {});
  }
  const frames = Math.ceil(decoded.duration * rate);
  if (frames === 0) return new Uint8Array(0);
  const offline = new OfflineAudioContext(1, frames, rate);
  const source = offline.createBufferSource();
  source.buffer = decoded;
  source.connect(offline.destination);
  source.start();
  const samples = (await offline.startRendering()).getChannelData(0);
  const pcm = new Uint8Array(samples.length * 2);
  const view = new DataView(pcm.buffer);
  for (let i = 0; i < samples.length; i++) {
    const sample = Math.max(-1, Math.min(1, samples[i]!));
    view.setInt16(i * 2, sample < 0 ? sample * 0x8000 : sample * 0x7fff, true);
  }
  return pcm;
}

function base64(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}

function readinessOf(status: VoiceStatus | null, error: string | null, checked: boolean): Readiness {
  if (!inDesktop) return { kind: "unavailable", message: "Dictation is available in the Orole OS desktop app." };
  if (!checked) return { kind: "checking" };
  if (!status) return error ? { kind: "unavailable", message: `Could not check dictation: ${error}` } : { kind: "checking" };
  if (!status.supported) return { kind: "unsupported" };
  if (status.ready) return { kind: "ready", minutes: Math.round((status.maxSeconds || DEFAULT_LIMITS.maxSeconds) / 60) };
  const downloading = status.models.find((model) => model.state === "downloading");
  if (downloading) return { kind: "downloading", model: downloading };
  return { kind: "no-model", model: status.models.find((model) => model.id === status.model) ?? status.models[0] };
}

/**
 * Records the microphone with MediaRecorder, converts the finished recording to PCM, hands it to the daemon, and
 * delivers the transcript to the caller's editable draft. Speech recognition runs on this computer with a model the
 * user downloaded; neither audio nor text leaves it. The microphone button starts recording at once when a model is
 * ready; otherwise it opens a popover that explains what is missing. Nothing is sent to the agent automatically.
 */
export function ComposerVoice({ disabled, compact = false, quiet = false, onTranscript, onActiveChange }: {
  disabled: boolean;
  /** A round icon button and an inline one-line recording state, for a single-line composer. */
  compact?: boolean;
  /** With `compact`: small ghost round buttons instead of the primary-filled large ones, for a toolbar that has its own Send button. */
  quiet?: boolean;
  onTranscript: (text: string) => void;
  onActiveChange: (active: boolean) => void;
}) {
  const [open, setOpen] = useState(false);
  const [state, setState] = useState<VoiceState>("idle");
  const [seconds, setSeconds] = useState(0);
  const [checked, setChecked] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [needsSetup, setNeedsSetup] = useState(false);
  const status = useDictation((s) => s.status);
  const loadError = useDictation((s) => s.error);
  const shown = readinessOf(status, loadError, checked);
  const target = shown.kind === "no-model" ? shown.model : undefined;
  const downloadBusy = useDictation((s) => !!target && !!s.busy[target.id]);
  const downloadError = useDictation((s) => target && s.actionError?.target === target.id ? s.actionError.message : null);
  const active = useRef<Run | null>(null);
  const limits = useRef(DEFAULT_LIMITS);
  const callbacks = useRef({ onTranscript, onActiveChange });
  callbacks.current = { onTranscript, onActiveChange };

  // Late recorder or daemon results must never edit the next task's draft, and the microphone must never stay open.
  // An unmount that ends a recording tells the parent it is over, or a parent that hides Send while recording would keep it hidden.
  useEffect(() => () => {
    const run = active.current;
    active.current = null;
    if (run) {
      run.discarded = true;
      release(run);
      discard(run.id);
      callbacks.current.onActiveChange(false);
    }
  }, []);

  /**
   * The microphone button: records at once when dictation is ready, and only otherwise opens the popover that says
   * why it cannot (no model yet, a download running, an unsupported computer).
   */
  const toggle = (next: boolean) => {
    if (!next) { setOpen(false); return; }
    if (disabled || active.current) return;
    void (async () => {
      const store = useDictation.getState();
      if (inDesktop && !store.status) await store.load();
      const now = useDictation.getState();
      if (readinessOf(now.status, now.error, true).kind === "ready") void start();
      else { setChecked(false); setOpen(true); }
    })();
  };
  // While the disclosure is open it follows the daemon live, so a running download shows its progress.
  useEffect(() => {
    if (!open || !inDesktop) return;
    let live = true;
    const stopSync = useDictation.getState().startSync();
    void useDictation.getState().load().then(() => { if (live) setChecked(true); });
    return () => { live = false; stopSync(); };
  }, [open]);

  const end = (run: Run) => {
    if (active.current !== run) return false;
    active.current = null;
    run.discarded = true;
    release(run);
    setState("idle");
    callbacks.current.onActiveChange(false);
    return true;
  };

  const fail = (run: Run, message: string, setup = false) => {
    if (!end(run)) return;
    setError(message);
    setNeedsSetup(setup);
  };

  const transcribe = async (run: Run, blob: Blob) => {
    try {
      if (blob.size === 0) return fail(run, NO_AUDIO);
      if (blob.size > MAX_RECORDING_BYTES) return fail(run, "That recording is too large to transcribe. Record a shorter message.");
      let pcm: Uint8Array;
      try { pcm = await decodePCM(blob, limits.current.sampleRate); }
      catch { return fail(run, "The recording could not be read. Try again, or type your message."); }
      if (active.current !== run) return;
      if (pcm.length === 0) return fail(run, NO_AUDIO);
      const id = crypto.randomUUID();
      run.id = id;
      // Whole samples per part, each within the daemon's part limit.
      const step = limits.current.partBytes - (limits.current.partBytes % 2);
      for (let part = 0; part * step < pcm.length; part++) {
        await Dictation.upload({ id, part, pcmBase64: base64(pcm.subarray(part * step, (part + 1) * step)) });
        // Cancelled while this part was in flight: cancel's discard may have arrived first.
        if (active.current !== run) return discard(id);
      }
      const result = await Dictation.transcribe({ id });
      if (!end(run)) return;
      const text = result.text.trim();
      setNotice(text
        ? `Dictation added to your draft. Review and edit it before sending.${run.limitNotice}`
        : `No speech was recognized. Your draft is unchanged.${run.limitNotice}`);
      if (text) callbacks.current.onTranscript(text);
    } catch (failure) {
      if (active.current !== run) return;
      discard(run.id);
      const { code, message } = dictationError(failure);
      if (code === "not_ready") void useDictation.getState().load();
      fail(run, code ? message : `Dictation failed: ${message}`, code === "not_ready");
    }
  };

  const start = async () => {
    if (disabled || active.current) return;
    setError("");
    setNotice("");
    setNeedsSetup(false);
    setSeconds(0);
    setOpen(false);
    const current = useDictation.getState().status;
    limits.current = {
      maxSeconds: current?.maxSeconds || DEFAULT_LIMITS.maxSeconds,
      sampleRate: current?.sampleRate || DEFAULT_LIMITS.sampleRate,
      partBytes: current?.partBytes || DEFAULT_LIMITS.partBytes,
    };
    const run: Run = { chunks: [], bytes: 0, startedAt: 0, discarded: false, limitNotice: "" };
    active.current = run;
    setState("starting");
    callbacks.current.onActiveChange(true);
    try {
      if (!navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === "undefined") {
        throw new Error("this window cannot record audio");
      }
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      run.stream = stream;
      if (active.current !== run) { release(run); return; }
      const type = pickMime();
      const recorder = new MediaRecorder(stream, { ...(type ? { mimeType: type } : {}), audioBitsPerSecond: AUDIO_BITS_PER_SECOND });
      run.recorder = recorder;
      recorder.ondataavailable = ({ data }) => {
        if (run.discarded || data.size === 0) return;
        if (run.bytes + data.size > MAX_RECORDING_BYTES) {
          run.limitNotice = " The recording reached its size limit, so the end was left out.";
          stop(run);
          return;
        }
        run.chunks.push(data);
        run.bytes += data.size;
      };
      recorder.onerror = () => fail(run, "Recording failed. Check your microphone and try again.");
      recorder.onstop = () => {
        if (run.discarded || active.current !== run) return;
        const blob = new Blob(run.chunks, { type: recorder.mimeType || type || "audio/webm" });
        // The microphone is released before the recording is converted.
        run.stream?.getTracks().forEach((track) => track.stop());
        void transcribe(run, blob);
      };
      run.startedAt = Date.now();
      recorder.start(SLICE_MS);
      setState("recording");
      // Loads the model while the user speaks, so the transcript is not held up by it. Failures surface on transcribe.
      Dictation.prepare().catch(() => {});
      run.timer = window.setInterval(() => {
        const elapsed = Math.floor((Date.now() - run.startedAt) / 1000);
        setSeconds(elapsed);
        if (elapsed >= limits.current.maxSeconds && !run.limitNotice) {
          run.limitNotice = ` Recording stopped at the ${Math.round(limits.current.maxSeconds / 60)}-minute limit.`;
          stop(run);
        }
      }, 1000);
    } catch (failure) {
      fail(run, micError(failure));
    }
  };

  /** Stop recording and hand the audio over for transcription. */
  const stop = (run: Run | null = active.current) => {
    if (!run || active.current !== run || !run.recorder || run.recorder.state === "inactive") return;
    window.clearInterval(run.timer);
    run.timer = undefined;
    setState("transcribing");
    try { run.recorder.stop(); }
    catch (failure) { fail(run, `Could not finish recording: ${errText(failure)}`); }
  };

  const cancel = () => {
    const run = active.current;
    if (!run) return;
    const sending = state === "transcribing";
    end(run);
    discard(run.id);
    setNotice(sending ? "Dictation cancelled. Nothing was added to your draft." : "Dictation cancelled. Your draft is unchanged.");
  };

  const openVoiceSettings = () => {
    setOpen(false);
    useUI.getState().openSettings("voice");
  };
  const settingsButton = <Button type="button" size="sm" variant="outline" onClick={openVoiceSettings}>Voice settings</Button>;

  const messages = <>
    {notice && <p role="status" className="text-xs text-muted-foreground">{notice}</p>}
    {error && <p role="alert" className="text-xs text-destructive-foreground">{error}</p>}
    {error && needsSetup && <div>{settingsButton}</div>}
  </>;
  const disclosure = <PopoverPopup side="top" align={compact ? "end" : "start"} className="w-80">
    <div className="flex flex-col gap-3">
      <PopoverTitle>Dictate a message</PopoverTitle>
      {shown.kind === "checking" && <p role="status" className="text-sm text-muted-foreground">Checking dictation…</p>}
      {shown.kind === "unavailable" && <p role="status" className="text-sm">{shown.message}</p>}
      {shown.kind === "unsupported" && <p role="status" className="text-sm">Dictation needs Windows on an x64 processor. The speech engine has no build for this computer.</p>}
      {shown.kind === "no-model" && <>
        <p role="status" className="text-sm">Dictation runs on this computer with a speech model you download once.</p>
        {target?.state === "failed" && target.error && <p role="alert" className="text-xs text-destructive-foreground">{target.error}</p>}
        {downloadError && <p role="alert" className="text-xs text-destructive-foreground">{downloadError}</p>}
        <div className="flex flex-wrap gap-2">
          {target && <Button type="button" size="sm" loading={downloadBusy} onClick={() => void useDictation.getState().download(target.id)}>
            Download {target.name.split(" ")[0]} ({megabytes(target.downloadBytes)} MB)
          </Button>}
          {settingsButton}
        </div>
      </>}
      {shown.kind === "downloading" && <>
        <p role="status" className="text-sm">Downloading {shown.model.name}: {megabytes(shown.model.receivedBytes)} of {megabytes(shown.model.totalBytes || shown.model.downloadBytes)} MB. Dictation is ready when it finishes.</p>
        <div>{settingsButton}</div>
      </>}
      {shown.kind === "ready" && <>
        <p className="text-sm">Dictation is ready. It runs on this computer; neither the recording nor the text is saved.</p>
        <Button type="button" size="sm" onClick={() => void start()}>Start dictation</Button>
      </>}
    </div>
  </PopoverPopup>;
  const elapsed = `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
  const statusText = state === "starting" ? "Starting microphone…" : state === "recording" ? "Recording" : "Transcribing…";

  if (compact) {
    return <div className="relative flex shrink-0 items-center gap-2">
      {(notice || error) && <div className="absolute end-0 bottom-full mb-2 flex w-72 flex-col gap-1 rounded-lg border bg-popover p-2 shadow-lg/5">{messages}</div>}
      {state === "idle" ? <Popover open={open} onOpenChange={toggle}>
        <PopoverTrigger render={<Button type="button" size={quiet ? "icon-sm" : "icon-lg"} variant={quiet ? "ghost" : "default"} className="rounded-full" disabled={disabled} aria-label="Dictate a message" />}><MicIcon aria-hidden /></PopoverTrigger>
        {disclosure}
      </Popover> : <>
        <MicIcon aria-hidden className="size-4 text-warning" />
        <span role="status" className="text-xs">{statusText}</span>
        <span aria-label="Dictation elapsed time" className="font-mono text-xs tabular-nums">{elapsed}</span>
        <Button type="button" size={quiet ? "icon-sm" : "icon-lg"} variant={quiet ? "outline" : "default"} className="rounded-full" aria-label="Stop dictation" onClick={() => stop()} disabled={state !== "recording"}><SquareIcon aria-hidden className="fill-current" /></Button>
        <Button type="button" size={quiet ? "icon-sm" : "icon-lg"} variant="ghost" className="rounded-full" aria-label="Cancel dictation" onClick={cancel}><XIcon aria-hidden /></Button>
      </>}
    </div>;
  }

  return <div className="flex min-w-0 flex-col gap-2">
    {state === "idle" ? <Popover open={open} onOpenChange={toggle}>
      <PopoverTrigger render={<Button type="button" size="sm" variant="ghost" disabled={disabled} aria-label="Dictate a message" />}><MicIcon aria-hidden data-icon="inline-start" />Dictate</PopoverTrigger>
      {disclosure}
    </Popover> : <div className="flex flex-wrap items-center gap-2">
      <MicIcon aria-hidden className="size-4 text-warning" />
      <span role="status" className="text-xs">{statusText}</span>
      <span aria-label="Dictation elapsed time" className="font-mono text-xs tabular-nums">{elapsed}</span>
      <Button type="button" size="xs" variant="outline" onClick={() => stop()} disabled={state !== "recording"}><SquareIcon aria-hidden data-icon="inline-start" />Stop</Button>
      <Button type="button" size="xs" variant="ghost" onClick={cancel}>Cancel dictation</Button>
    </div>}
    {messages}
  </div>;
}
