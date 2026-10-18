import { useEffect, useRef, useState } from "react";
import { mount } from "ascii.rest";
import { Upload } from "lucide-react";
import { customBackgroundPiece, importBackground } from "@/components/ascii/custom-background";
import { Button } from "@/components/ui/button";
import { useSettings } from "@/stores/settings";
import { NO_SCENE, SceneGrid } from "./SceneGrid";

export function ScenePicker() {
  const scene = useSettings((state) => state.activeAsciiScene);
  const enabled = useSettings((state) => state.ascii.enabled);
  const loaded = useSettings((state) => state.loaded);
  const set = useSettings((state) => state.set);
  const custom = useSettings((state) => state.ascii.customBackground);
  const fileInput = useRef<HTMLInputElement>(null);
  const preview = useRef<HTMLCanvasElement>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (custom && preview.current) return mount(preview.current, customBackgroundPiece(custom), { fps: 0 });
  }, [custom]);
  return (
    <div className="flex flex-col gap-3 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <input ref={fileInput} type="file" accept="image/png,image/jpeg,image/webp" className="hidden"
          aria-label="Upload background image" disabled={!loaded || busy}
          onChange={async (event) => {
            const file = event.target.files?.[0];
            event.target.value = "";
            if (!file) return;
            setBusy(true);
            setError(null);
            try {
              const background = await importBackground(file);
              set("ascii", { customBackground: background, enabled: true });
            } catch (cause) {
              setError(cause instanceof Error ? cause.message : "Could not import this image. Try another image.");
            } finally {
              setBusy(false);
            }
          }} />
        <Button variant="outline" size="sm" disabled={!loaded || busy} onClick={() => fileInput.current?.click()}>
          <Upload aria-hidden="true" data-icon="inline-start" />
          {busy ? "Converting image…" : custom ? "Replace image" : "Upload image"}
        </Button>
        {custom && <Button variant="ghost" size="sm" disabled={busy} onClick={() => {
          set("ascii", { customBackground: null });
          setError(null);
        }}>Remove image</Button>}
        <span className="text-xs text-muted-foreground">PNG, JPEG, or WebP · up to 10 MB</span>
      </div>
      <p className="text-xs text-muted-foreground">Images are center-cropped and converted to a still, colored-dot scene. Choosing a built-in scene replaces your upload.</p>
      {busy && <p role="status" className="text-xs text-muted-foreground">Converting to the Home background style…</p>}
      {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
      {custom && <div className="flex min-w-0 flex-col gap-1">
        <canvas ref={preview} aria-label="Converted background preview" role="img" className="w-full max-w-80 rounded-md" />
        <span className="truncate text-xs text-muted-foreground">{custom.name} · Current background</span>
      </div>}
      <SceneGrid selected={custom ? null : enabled ? scene : NO_SCENE} disabled={!loaded || busy}
        onSelect={(id) => set("ascii", id === NO_SCENE ? { enabled: false } : { scene: id, enabled: true })} />
    </div>
  );
}
