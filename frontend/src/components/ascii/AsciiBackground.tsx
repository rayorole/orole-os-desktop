import { useEffect, useRef, useState } from "react";
import { mount } from "ascii.rest";
import { useSettings } from "@/stores/settings";
import { useUI } from "@/stores/ui";
import { customBackgroundPiece } from "./custom-background";
import { loadScene } from "./scene-loader";

export function useMedia(q: string) {
  const [m, setM] = useState(() => matchMedia(q).matches);
  useEffect(() => {
    const mq = matchMedia(q);
    const f = () => setM(mq.matches);
    mq.addEventListener("change", f);
    return () => mq.removeEventListener("change", f);
  }, [q]);
  return m;
}

/** Low-power heuristic: few cores, or a discharging battery under 20%. */
export function useLowPower() {
  const [low, setLow] = useState(() => (navigator.hardwareConcurrency ?? 8) <= 2);
  useEffect(() => {
    type Bat = { level: number; charging: boolean; addEventListener: (e: string, f: () => void) => void; removeEventListener: (e: string, f: () => void) => void };
    const nav = navigator as Navigator & { getBattery?: () => Promise<Bat> };
    let bat: Bat | undefined;
    const f = () => bat && setLow((navigator.hardwareConcurrency ?? 8) <= 2 || (!bat.charging && bat.level < 0.2));
    nav.getBattery?.().then((b) => {
      bat = b;
      f();
      b.addEventListener("levelchange", f);
      b.addEventListener("chargingchange", f);
    }).catch(() => {});
    return () => {
      bat?.removeEventListener("levelchange", f);
      bat?.removeEventListener("chargingchange", f);
    };
  }, []);
  return low;
}

export interface EffectiveMotion {
  animate: boolean;
  fps: number;
  reason: "ok" | "disabled" | "static" | "paused" | "reduced-motion" | "unfocused" | "low-power" | "intensity";
}

/** Pure decision function so the policy is unit-testable. */
export function decideMotion(a: {
  enabled: boolean; staticOnly: boolean; paused: boolean; fps: number; intensity: number;
  pauseWhenUnfocused: boolean; focused: boolean; reduceMotion: boolean; lowPower: boolean;
}): EffectiveMotion {
  if (!a.enabled) return { animate: false, fps: 0, reason: "disabled" };
  if (a.staticOnly) return { animate: false, fps: 0, reason: "static" };
  if (a.paused) return { animate: false, fps: 0, reason: "paused" };
  if (a.reduceMotion) return { animate: false, fps: 0, reason: "reduced-motion" };
  if (a.pauseWhenUnfocused && !a.focused) return { animate: false, fps: 0, reason: "unfocused" };
  let fps = Math.round(Math.min(30, Math.max(1, a.fps)) * Math.min(1, Math.max(0, a.intensity)));
  if (fps < 1) return { animate: false, fps: 0, reason: "intensity" };
  if (a.lowPower) fps = Math.min(fps, 6);
  return { animate: true, fps, reason: a.lowPower ? "low-power" : "ok" };
}

/**
 * The harbour band across the top of Home (Shipyard's "Labs" layout): an ascii.rest scene
 * cropped like object-fit: cover, dissolving into the page background at its foot. One canvas
 * per window, never per widget. Holds a still first frame when paused, unfocused, low-power or
 * when reduced motion is requested.
 */
export const BAND_HEIGHT = "clamp(220px, 50vh, 500px)";

export function AsciiBand({ position = 0.42, fade = 0.45 }: { position?: number; fade?: number }) {
  const s = useSettings((x) => x.ascii);
  const scene = useSettings((x) => x.activeAsciiScene);
  const loaded = useSettings((x) => x.loaded);
  const a11y = useSettings((x) => x.accessibility);
  const focused = useUI((x) => x.windowFocused);
  const sysReduced = useMedia("(prefers-reduced-motion: reduce)");
  const lowPower = useLowPower();
  const wrap = useRef<HTMLDivElement>(null);
  const ref = useRef<HTMLCanvasElement>(null);
  const [box, setBox] = useState({ w: 0, h: 0 });
  const reduce = a11y.reduceMotion === "on" || (a11y.reduceMotion === "system" && s.respectReducedMotion && sysReduced);
  const m = decideMotion({ ...s, focused, reduceMotion: reduce, lowPower });

  useEffect(() => {
    const el = wrap.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setBox({ w: el.clientWidth, h: el.clientHeight }));
    ro.observe(el);
    return () => ro.disconnect();
  }, [s.enabled, loaded]);

  useEffect(() => {
    const el = ref.current;
    if (!el || !s.enabled || !loaded) return;
    if (s.customBackground) {
      return mount(el, customBackgroundPiece(s.customBackground), { fps: 0 });
    }
    let stop: (() => void) | undefined;
    let live = true;
    const loading = loadScene(scene);
    if (!loading) return;
    loading.then((piece) => {
      if (live) stop = mount(el, piece, { fps: m.fps, motion: true });
    });
    return () => {
      live = false;
      stop?.();
    };
  }, [scene, s.customBackground, s.enabled, loaded, m.fps]);

  if (!s.enabled || !loaded) return null;
  // Scenes are 2:1; cover the band, crop vertically at `position`.
  const width = Math.max(box.w, box.h * 2);
  const top = (box.h - width / 2) * position;
  const mask = `linear-gradient(to bottom, #000 ${Math.round((1 - fade) * 100)}%, transparent 100%)`;
  return (
    <div ref={wrap} aria-hidden="true" data-testid="ascii-bg" data-motion={s.customBackground ? "static" : m.reason} className="pointer-events-none absolute inset-x-0 top-0 overflow-hidden"
      style={{ height: BAND_HEIGHT, maskImage: mask, WebkitMaskImage: mask }}>
      <canvas ref={ref} className="absolute" style={{ width, left: (box.w - width) / 2, top, opacity: s.opacity * (0.35 + 0.65 * s.intensity) }} />
    </div>
  );
}
