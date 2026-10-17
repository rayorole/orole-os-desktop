import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ModelStatus, UploadRequest, VoiceStatus } from "@/mygo";

const api = vi.hoisted(() => ({
  status: vi.fn(), download: vi.fn(), prepare: vi.fn(), upload: vi.fn<(request: UploadRequest) => Promise<void>>(), transcribe: vi.fn(), discard: vi.fn(),
  openSettings: vi.fn(), getUserMedia: vi.fn(), changed: [] as ((status: unknown) => void)[],
}));
const env = vi.hoisted(() => ({ desktop: true }));
vi.mock("@/lib/backend", () => ({ get inDesktop() { return env.desktop; } }));
vi.mock("@/mygo", () => ({
  Dictation: { status: api.status, download: api.download, prepare: api.prepare, upload: api.upload, transcribe: api.transcribe, discard: api.discard },
  events: { dictationChanged: { on: (cb: (status: unknown) => void) => { api.changed.push(cb); return () => { api.changed = api.changed.filter((c) => c !== cb); }; } } },
}));
vi.mock("@/stores/ui", () => ({ useUI: { getState: () => ({ openSettings: api.openSettings }) } }));
import { useDictation } from "@/stores/dictation";
import { ComposerVoice } from "./ComposerVoice";

class RecorderDouble {
  static instances: RecorderDouble[] = [];
  static isTypeSupported = vi.fn((type: string) => type === "audio/webm;codecs=opus");
  state: "inactive" | "recording" = "inactive";
  mimeType: string;
  ondataavailable: ((event: { data: Blob }) => void) | null = null;
  onstop: (() => void) | null = null;
  onerror: (() => void) | null = null;
  start = vi.fn(() => { this.state = "recording"; });
  /** Like the real recorder: flush the last chunk, then report the stop. */
  stop = vi.fn(() => {
    this.state = "inactive";
    this.ondataavailable?.({ data: new Blob(["voice"], { type: this.mimeType }) });
    this.onstop?.();
  });
  readonly stream: unknown;
  readonly options: { mimeType?: string; audioBitsPerSecond?: number };
  constructor(stream: unknown, options: { mimeType?: string; audioBitsPerSecond?: number }) {
    this.stream = stream;
    this.options = options;
    this.mimeType = options.mimeType ?? "";
    RecorderDouble.instances.push(this);
  }
}

/** What the browser's decoder makes of the recording: its length, and the samples rendering produces. */
const audio = { duration: 0.5, decode: vi.fn<(data: ArrayBuffer) => Promise<unknown>>(), rendered: [] as { channels: number; frames: number; rate: number }[], closed: vi.fn() };
class AudioContextDouble {
  decodeAudioData(data: ArrayBuffer) { return audio.decode(data); }
  close() { audio.closed(); return Promise.resolve(); }
}
class OfflineAudioContextDouble {
  readonly destination = {};
  readonly frames: number;
  constructor(channels: number, frames: number, rate: number) {
    this.frames = frames;
    audio.rendered.push({ channels, frames, rate });
  }
  createBufferSource() { return { buffer: null, connect: vi.fn(), start: vi.fn() }; }
  startRendering() {
    const samples = new Float32Array(this.frames);
    samples.set([0.5, -1, 1, -2].slice(0, this.frames)); // -2: out of range, clipped
    return Promise.resolve({ getChannelData: () => samples });
  }
}

const track = { stop: vi.fn() };
const stream = { getTracks: () => [track] };
const model = (over: Partial<ModelStatus> = {}): ModelStatus => ({
  id: "parakeet-tdt-0.6b-v3", name: "Parakeet TDT 0.6B v3", description: "Fast.", languageChoice: false,
  sizeBytes: 670478772, downloadBytes: 670478772, state: "ready", receivedBytes: 0, totalBytes: 0, license: "CC BY 4.0", ...over,
});
const whisper = model({ id: "whisper-large-v3-turbo", name: "Whisper large-v3 turbo", languageChoice: true, sizeBytes: 1036613791, downloadBytes: 1036613791, state: "absent" });
const status = (over: Partial<VoiceStatus> = {}): VoiceStatus => ({
  supported: true, ready: true, model: "parakeet-tdt-0.6b-v3", language: "", models: [model(), whisper], languages: [],
  maxSeconds: 120, sampleRate: 16000, partBytes: 1 << 20, ...over,
});
const absent = () => status({ ready: false, model: "", models: [model({ state: "absent" }), whisper] });
interface Reply { text: string; model: string }
const reply = (text: string): Reply => ({ text, model: "parakeet-tdt-0.6b-v3" });
const emit = (next: VoiceStatus) => act(() => api.changed.forEach((cb) => cb(next)));
const pcmOf = ({ pcmBase64 }: UploadRequest) => Uint8Array.from(atob(pcmBase64), (c) => c.charCodeAt(0));

/** A draft box beside the voice control, like the composer that embeds it. */
function Harness({ compact = false, onActive = () => {}, onSend = () => {} }: { compact?: boolean; onActive?: (active: boolean) => void; onSend?: () => void }) {
  const [text, setText] = useState("");
  const [active, setActive] = useState(false);
  return <div>
    <textarea aria-label="Message the agent" value={text} onChange={(event) => setText(event.target.value)} />
    <button type="button" disabled={active} onClick={onSend}>Send</button>
    <ComposerVoice compact={compact} disabled={false}
      onActiveChange={(value) => { setActive(value); onActive(value); }}
      onTranscript={(value) => setText((current) => current ? `${current}${/\s$/.test(current) ? "" : " "}${value}` : value)} />
  </div>;
}

const draft = () => screen.getByLabelText("Message the agent");
const mic = () => fireEvent.click(screen.getByRole("button", { name: "Dictate a message" }));
async function startRecording() {
  mic();
  await waitFor(() => expect(screen.getByRole("button", { name: "Stop" })).toBeEnabled());
  return RecorderDouble.instances.at(-1)!;
}

beforeEach(() => {
  vi.clearAllMocks();
  env.desktop = true;
  api.changed = [];
  useDictation.setState({ status: null, loading: false, error: null, busy: {}, actionError: null });
  RecorderDouble.instances = [];
  RecorderDouble.isTypeSupported.mockImplementation((type: string) => type === "audio/webm;codecs=opus");
  vi.stubGlobal("MediaRecorder", RecorderDouble);
  vi.stubGlobal("AudioContext", AudioContextDouble);
  vi.stubGlobal("OfflineAudioContext", OfflineAudioContextDouble);
  audio.duration = 0.5;
  audio.rendered = [];
  audio.decode.mockImplementation(() => Promise.resolve({ duration: audio.duration, numberOfChannels: 2, sampleRate: 48000 }));
  Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { getUserMedia: api.getUserMedia } });
  api.getUserMedia.mockResolvedValue(stream);
  api.status.mockResolvedValue(status());
  api.prepare.mockResolvedValue(undefined);
  api.upload.mockResolvedValue(undefined);
  api.discard.mockResolvedValue(undefined);
  api.transcribe.mockResolvedValue(reply("Recognized sentence."));
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  Reflect.deleteProperty(navigator, "mediaDevices");
});

describe("consent", () => {
  it("starts recording at once when a model is ready, with no menu in between", async () => {
    render(<Harness />);
    mic();
    await waitFor(() => expect(api.getUserMedia).toHaveBeenCalledOnce());
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("button", { name: "Start dictation" })).toBeNull();
    await waitFor(() => expect(screen.getByRole("button", { name: "Stop" })).toBeEnabled());
    expect(api.prepare).toHaveBeenCalledOnce();
  });

  it("offers to download the first model when none is ready, then follows the download until dictation is ready", async () => {
    api.status.mockResolvedValue(absent());
    api.download.mockResolvedValue(status({ ready: false, model: "", models: [model({ state: "downloading", totalBytes: 670478772 }), whisper] }));
    render(<Harness />);
    mic();
    fireEvent.click(await screen.findByRole("button", { name: "Download Parakeet (639.4 MB)" }));
    expect(api.download).toHaveBeenCalledExactlyOnceWith({ id: "parakeet-tdt-0.6b-v3" });
    expect(await screen.findByText(/Downloading Parakeet TDT 0\.6B v3: 0\.0 of 639\.4 MB/)).toBeInTheDocument();
    emit(status({ ready: false, model: "", models: [model({ state: "downloading", receivedBytes: 327584768, totalBytes: 670478772 }), whisper] }));
    expect(screen.getByText(/312\.4 of 639\.4 MB/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start dictation" })).not.toBeInTheDocument();
    emit(status());
    expect(screen.getByRole("button", { name: "Start dictation" })).toBeInTheDocument();
    expect(api.getUserMedia).not.toHaveBeenCalled();
  });

  it("includes the speech engine in the download size when it is missing, and links to the voice settings", async () => {
    api.status.mockResolvedValue(status({ ready: false, model: "", models: [model({ state: "absent", downloadBytes: 678014852 }), whisper] }));
    render(<Harness />);
    mic();
    expect(await screen.findByRole("button", { name: "Download Parakeet (646.6 MB)" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Voice settings" }));
    expect(api.openSettings).toHaveBeenCalledExactlyOnceWith("voice");
  });

  it("shows why a download failed and the daemon's refusal of a new one", async () => {
    api.status.mockResolvedValue(status({ ready: false, model: "", models: [model({ state: "failed", error: "The download was interrupted." }), whisper] }));
    api.download.mockRejectedValue(new Error("dictation_busy: Another download is running. Wait for it to finish."));
    render(<Harness />);
    mic();
    expect(await screen.findByRole("alert")).toHaveTextContent("The download was interrupted.");
    fireEvent.click(screen.getByRole("button", { name: "Download Parakeet (639.4 MB)" }));
    expect(await screen.findByText("Another download is running. Wait for it to finish.")).toBeInTheDocument();
  });

  it("explains a computer the speech engine cannot run on", async () => {
    api.status.mockResolvedValue(status({ supported: false, ready: false }));
    render(<Harness />);
    mic();
    expect(await screen.findByText(/Dictation needs Windows on an x64 processor/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Download/ })).not.toBeInTheDocument();
  });

  it("reports a status failure instead of offering to record", async () => {
    api.status.mockRejectedValue(new Error("internal: daemon unavailable: pipe closed"));
    render(<Harness />);
    mic();
    expect(await screen.findByText("Could not check dictation: daemon unavailable: pipe closed")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start dictation" })).not.toBeInTheDocument();
  });
});

describe("recording", () => {
  it("records, then Stop uploads 16 kHz PCM in parts and transcribes into an editable draft that is never sent", async () => {
    api.status.mockResolvedValue(status({ partBytes: 6001 }));
    const active = vi.fn();
    const send = vi.fn();
    render(<Harness onActive={active} onSend={send} />);
    fireEvent.change(draft(), { target: { value: "Existing draft." } });
    const recorder = await startRecording();
    expect(api.getUserMedia).toHaveBeenCalledExactlyOnceWith({ audio: true });
    expect(recorder.options).toEqual({ mimeType: "audio/webm;codecs=opus", audioBitsPerSecond: 32_000 });
    expect(recorder.start).toHaveBeenCalledOnce();
    expect(api.prepare).toHaveBeenCalledOnce(); // the model loads while the user speaks
    expect(active).toHaveBeenLastCalledWith(true);
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();

    const pending = Promise.withResolvers<Reply>();
    api.transcribe.mockReturnValue(pending.promise);
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(await screen.findByText("Transcribing…")).toBeInTheDocument();
    expect(recorder.stop).toHaveBeenCalledOnce();
    expect(track.stop).toHaveBeenCalled(); // the microphone is released before the upload
    await waitFor(() => expect(api.transcribe).toHaveBeenCalledOnce());

    expect(audio.rendered).toEqual([{ channels: 1, frames: 8000, rate: 16000 }]);
    // 0.5 s at 16 kHz is 16000 bytes: parts of whole samples within the 6001-byte limit, in order, under one id.
    const calls = api.upload.mock.calls;
    expect(calls.map(([request]) => request.part)).toEqual([0, 1, 2]);
    expect(calls.map(([request]) => pcmOf(request).length)).toEqual([6000, 6000, 4000]);
    const id = calls[0]![0].id;
    expect(id).toMatch(/^[A-Za-z0-9_-]{8,64}$/);
    for (const call of calls) expect(call[0]).toMatchObject({ id });
    expect(Array.from(new Int16Array(pcmOf(calls[0]![0]).buffer, 0, 5))).toEqual([16383, -32768, 32767, -32768, 0]);
    expect(api.transcribe).toHaveBeenCalledWith({ id });
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    expect(draft()).toHaveValue("Existing draft.");

    await act(async () => pending.resolve(reply("  Recognized sentence.  ")));
    expect(draft()).toHaveValue("Existing draft. Recognized sentence.");
    expect(active).toHaveBeenLastCalledWith(false);
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
    expect(screen.getByRole("status")).toHaveTextContent("Dictation added to your draft. Review and edit it before sending.");
    fireEvent.change(draft(), { target: { value: "Edited transcript" } });
    expect(send).not.toHaveBeenCalled();
    expect(api.discard).not.toHaveBeenCalled();
  });

  it("falls back to a supported container", async () => {
    RecorderDouble.isTypeSupported.mockImplementation((type: string) => type === "audio/mp4");
    render(<Harness />);
    const recorder = await startRecording();
    expect(recorder.options.mimeType).toBe("audio/mp4");
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(draft()).toHaveValue("Recognized sentence."));
  });

  it("lets the browser pick a container when none of the preferred ones is supported", async () => {
    RecorderDouble.isTypeSupported.mockReturnValue(false);
    render(<Harness />);
    const recorder = await startRecording();
    expect(recorder.options).toEqual({ audioBitsPerSecond: 32_000 });
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(draft()).toHaveValue("Recognized sentence."));
  });

  it("shows the elapsed time and stops on its own at the maximum duration", async () => {
    render(<Harness />);
    await act(() => useDictation.getState().load());
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
    mic();
    await act(async () => {});
    const recorder = RecorderDouble.instances[0]!;
    await act(async () => { vi.advanceTimersByTime(2100); });
    expect(screen.getByLabelText("Dictation elapsed time")).toHaveTextContent("0:02");
    expect(recorder.stop).not.toHaveBeenCalled();
    await act(async () => { vi.advanceTimersByTime(118_000); });
    expect(recorder.stop).toHaveBeenCalledOnce();
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); }); // only the interval and Date are faked
    expect(api.transcribe).toHaveBeenCalledOnce();
    expect(draft()).toHaveValue("Recognized sentence.");
    expect(screen.getByRole("status")).toHaveTextContent("Recording stopped at the 2-minute limit.");
  });

  it("stops when the recording reaches the size cap and drops the excess", async () => {
    render(<Harness />);
    const recorder = await startRecording();
    act(() => recorder.ondataavailable?.({ data: new Blob([new Uint8Array((2 << 20) - 7)]) }));
    act(() => recorder.ondataavailable?.({ data: new Blob(["too much"]) }));
    await waitFor(() => expect(api.transcribe).toHaveBeenCalledOnce());
    expect(recorder.stop).toHaveBeenCalledOnce();
    expect(audio.decode.mock.calls[0]![0].byteLength).toBe((2 << 20) - 7 + "voice".length);
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("size limit"));
  });

  it("works in the compact layout", async () => {
    render(<Harness compact />);
    mic();
    fireEvent.click(await screen.findByRole("button", { name: "Stop dictation" }));
    await waitFor(() => expect(draft()).toHaveValue("Recognized sentence."));
    expect(screen.getByRole("button", { name: "Dictate a message" })).toBeInTheDocument();
  });
});

describe("cancel and teardown", () => {
  it("cancel discards the recording without sending any audio", async () => {
    const active = vi.fn();
    render(<Harness onActive={active} />);
    fireEvent.change(draft(), { target: { value: "Keep this" } });
    const recorder = await startRecording();
    fireEvent.click(screen.getByRole("button", { name: "Cancel dictation" }));
    expect(track.stop).toHaveBeenCalled();
    expect(recorder.stop).toHaveBeenCalledOnce();
    expect(api.upload).not.toHaveBeenCalled();
    expect(api.transcribe).not.toHaveBeenCalled();
    expect(draft()).toHaveValue("Keep this");
    expect(active).toHaveBeenLastCalledWith(false);
    expect(screen.getByRole("status")).toHaveTextContent("Dictation cancelled. Your draft is unchanged.");
    expect(screen.getByRole("button", { name: "Dictate a message" })).toBeInTheDocument();
  });

  it("cancel during the upload drops the uploaded part and sends nothing more", async () => {
    api.status.mockResolvedValue(status({ partBytes: 6000 }));
    const part = Promise.withResolvers<undefined>();
    api.upload.mockReturnValueOnce(part.promise);
    render(<Harness />);
    await startRecording();
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(api.upload).toHaveBeenCalledOnce());
    const { id } = api.upload.mock.calls[0]![0];
    fireEvent.click(screen.getByRole("button", { name: "Cancel dictation" }));
    expect(api.discard).toHaveBeenCalledWith({ id });
    await act(async () => part.resolve(undefined));
    expect(api.upload).toHaveBeenCalledOnce();
    expect(api.transcribe).not.toHaveBeenCalled();
    expect(screen.getByRole("status")).toHaveTextContent("Nothing was added to your draft.");
  });

  it("cancel while transcribing discards the recording and ignores the late result", async () => {
    const pending = Promise.withResolvers<Reply>();
    api.transcribe.mockReturnValue(pending.promise);
    render(<Harness />);
    await startRecording();
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(api.transcribe).toHaveBeenCalledOnce());
    fireEvent.click(screen.getByRole("button", { name: "Cancel dictation" }));
    expect(api.discard).toHaveBeenCalledWith(api.transcribe.mock.calls[0]![0]);
    await act(async () => pending.resolve(reply("Too late")));
    expect(draft()).toHaveValue("");
    expect(screen.getByRole("status")).toHaveTextContent("Nothing was added to your draft.");
  });

  it("releases the microphone and reports the recording as over when it unmounts while recording", async () => {
    const active = vi.fn();
    const view = render(<Harness onActive={active} />);
    const recorder = await startRecording();
    expect(active).toHaveBeenLastCalledWith(true);
    track.stop.mockClear();
    view.unmount();
    expect(track.stop).toHaveBeenCalled();
    expect(recorder.stop).toHaveBeenCalledOnce();
    expect(api.upload).not.toHaveBeenCalled();
    // A parent that outlives it (and hides Send while recording) must not be left believing a recording is going.
    expect(active).toHaveBeenLastCalledWith(false);
  });

  it("never delivers a transcript to a draft that has gone away (session switch)", async () => {
    const pending = Promise.withResolvers<Reply>();
    api.transcribe.mockReturnValue(pending.promise);
    const onTranscript = vi.fn();
    const view = render(<ComposerVoice disabled={false} onActiveChange={() => {}} onTranscript={onTranscript} />);
    mic();
    fireEvent.click(await waitFor(() => { const stop = screen.getByRole("button", { name: "Stop" }); expect(stop).toBeEnabled(); return stop; }));
    await waitFor(() => expect(api.transcribe).toHaveBeenCalledOnce());
    view.unmount();
    expect(api.discard).toHaveBeenCalledWith(api.transcribe.mock.calls[0]![0]);
    await act(async () => pending.resolve(reply("Wrong task")));
    expect(onTranscript).not.toHaveBeenCalled();
  });

  it("releases the microphone if the component goes away while the permission prompt is open", async () => {
    const grant = Promise.withResolvers<typeof stream>();
    api.getUserMedia.mockReturnValue(grant.promise);
    const view = render(<Harness />);
    mic();
    await waitFor(() => expect(api.getUserMedia).toHaveBeenCalledOnce());
    view.unmount();
    await act(async () => grant.resolve(stream));
    expect(track.stop).toHaveBeenCalled();
    expect(RecorderDouble.instances).toHaveLength(0);
  });
});

describe("errors", () => {
  const startAndStop = async () => {
    await startRecording();
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    return screen.findByRole("alert");
  };
  const start = async () => {
    mic();
    return screen.findByRole("alert");
  };

  it.each([
    ["denied", new DOMException("blocked", "NotAllowedError"), /Microphone access was denied.*Privacy & security > Microphone/],
    ["no microphone", new DOMException("none", "NotFoundError"), /No microphone was found/],
    ["busy microphone", new DOMException("busy", "NotReadableError"), /Another app may be using it/],
    ["unexpected", new Error("boom"), /Could not start recording: boom/],
  ])("explains a microphone failure: %s", async (_name, failure, message) => {
    api.getUserMedia.mockRejectedValue(failure);
    const active = vi.fn();
    render(<Harness onActive={active} />);
    expect(await start()).toHaveTextContent(message);
    expect(RecorderDouble.instances).toHaveLength(0);
    expect(active).toHaveBeenLastCalledWith(false);
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
  });

  it("says so when the window cannot record at all", async () => {
    Reflect.deleteProperty(navigator, "mediaDevices");
    render(<Harness />);
    expect(await start()).toHaveTextContent("this window cannot record audio");
  });

  it.each([
    ["too long", "dictation_too_long: That recording is longer than 120 seconds. Record a shorter message.", "longer than 120 seconds"],
    ["busy", "dictation_busy: Another recording is being transcribed. Try again in a moment.", "Another recording is being transcribed."],
    ["engine", "dictation_engine: The speech engine stopped. Try again.", "The speech engine stopped."],
    ["timeout", "dictation_timeout: Transcribing took too long. Record a shorter message.", "took too long"],
  ])("shows the daemon's sentence for %s without its machine prefix", async (_name, failure, message) => {
    api.transcribe.mockRejectedValue(new Error(failure));
    render(<Harness />);
    fireEvent.change(draft(), { target: { value: "Keep this" } });
    const alert = await startAndStop();
    expect(alert).toHaveTextContent(message);
    expect(alert).not.toHaveTextContent("dictation_");
    expect(draft()).toHaveValue("Keep this");
    expect(track.stop).toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Voice settings" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
  });

  it("checks again and offers the voice settings when the model went away after the disclosure", async () => {
    api.transcribe.mockRejectedValue(new Error("dictation_not_ready: Download a dictation model in Settings > Voice first."));
    render(<Harness />);
    await startRecording();
    api.status.mockResolvedValue(absent());
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Download a dictation model in Settings > Voice first.");
    await waitFor(() => expect(api.status).toHaveBeenCalledTimes(2));
    fireEvent.click(screen.getByRole("button", { name: "Voice settings" }));
    expect(api.openSettings).toHaveBeenCalledExactlyOnceWith("voice");
    mic();
    expect(await screen.findByRole("button", { name: "Download Parakeet (639.4 MB)" })).toBeInTheDocument();
  });

  it("describes any other failure plainly", async () => {
    api.upload.mockRejectedValue(new Error("internal: daemon unavailable"));
    render(<Harness />);
    expect(await startAndStop()).toHaveTextContent("Dictation failed: daemon unavailable");
    expect(api.transcribe).not.toHaveBeenCalled();
  });

  it("reports an empty transcript and leaves the draft untouched", async () => {
    api.transcribe.mockResolvedValue(reply("   "));
    render(<Harness />);
    fireEvent.change(draft(), { target: { value: "Keep this" } });
    await startRecording();
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("No speech was recognized. Your draft is unchanged."));
    expect(draft()).toHaveValue("Keep this");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("reports a recording that captured no audio without calling the daemon", async () => {
    render(<Harness />);
    const recorder = await startRecording();
    recorder.stop.mockImplementation(() => { recorder.state = "inactive"; recorder.onstop?.(); });
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("No audio was captured");
    expect(audio.decode).not.toHaveBeenCalled();
    expect(api.upload).not.toHaveBeenCalled();
  });

  it("reports a recording that decodes to no samples without calling the daemon", async () => {
    audio.duration = 0;
    render(<Harness />);
    expect(await startAndStop()).toHaveTextContent("No audio was captured");
    expect(api.upload).not.toHaveBeenCalled();
  });

  it("reports a recording the browser cannot decode", async () => {
    audio.decode.mockRejectedValue(new DOMException("bad data", "EncodingError"));
    render(<Harness />);
    expect(await startAndStop()).toHaveTextContent("The recording could not be read.");
    expect(api.upload).not.toHaveBeenCalled();
    expect(audio.closed).toHaveBeenCalled();
  });

  it("reports a recorder failure and releases the microphone", async () => {
    render(<Harness />);
    const recorder = await startRecording();
    act(() => recorder.onerror?.());
    expect(await screen.findByRole("alert")).toHaveTextContent("Recording failed");
    expect(track.stop).toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
  });
});

describe("outside the desktop app", () => {
  it("says dictation needs the desktop app instead of calling the daemon", async () => {
    env.desktop = false;
    render(<Harness />);
    mic();
    expect(await screen.findByText("Dictation is available in the Orole OS desktop app.")).toBeInTheDocument();
    expect(api.status).not.toHaveBeenCalled();
  });
});
