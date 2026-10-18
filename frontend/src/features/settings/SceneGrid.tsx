import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { defaultRangeExtractor, useVirtualizer } from "@tanstack/react-virtual";
import { ImageOff } from "lucide-react";
import { SCENES } from "@/components/ascii/scenes";
import { scenePreview } from "@/components/ascii/scene-previews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/** The id of the "no background" option. Not a scene: scenes never use it. */
export const NO_SCENE = "none";

const ITEMS = [{ id: NO_SCENE, label: "None" }, ...SCENES];

const MIN_TILE = 160;
const GAP = 8;
const THUMB_H = 84;
const LABEL_H = 32;
const ROW = THUMB_H + LABEL_H + 2; // plus the tile's border

/**
 * Picker for the Home background: None and every scene as a tile with a small static thumbnail. A listbox with one tab
 * stop and arrow-key movement; only the rows in view (plus the one holding the focus target) are mounted, so opening
 * Settings never draws or loads a scene. Choosing is Enter or Space; moving the focus changes nothing.
 */
export function SceneGrid({ selected, disabled, onSelect }: {
  /** The chosen scene id, NO_SCENE for none, or null when something else (an uploaded image) is the background. */
  selected: string | null;
  disabled?: boolean;
  onSelect: (id: string) => void;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(640);
  const columns = Math.max(1, Math.floor((width + GAP) / (MIN_TILE + GAP)));
  const rows = Math.ceil(ITEMS.length / columns);
  const [active, setActive] = useState(() => Math.max(0, ITEMS.findIndex((item) => item.id === selected)));
  const activeRow = Math.floor(active / columns);
  const moved = useRef(false);

  useEffect(() => {
    const el = scroller.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth || 640));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Keeps the row of the focus target mounted however far the list has been scrolled.
  const rangeExtractor = useMemo(() => (range: Parameters<typeof defaultRangeExtractor>[0]) => {
    const shown = defaultRangeExtractor(range);
    return shown.includes(activeRow) ? shown : [...shown, activeRow].sort((a, b) => a - b);
  }, [activeRow]);
  const virtual = useVirtualizer({
    count: rows, getScrollElement: () => scroller.current, estimateSize: () => ROW + GAP, overscan: 0, rangeExtractor,
    initialRect: { width: 640, height: 400 },
  });

  // After an arrow key the target row may not be mounted yet: focus it on the render that mounts it.
  useEffect(() => {
    if (!moved.current) return;
    const tile = scroller.current?.querySelector<HTMLElement>(`[data-index="${active}"]`);
    if (!tile) return;
    moved.current = false;
    tile.focus();
  });

  const onKeyDown = (event: KeyboardEvent) => {
    const step = { ArrowRight: 1, ArrowLeft: -1, ArrowDown: columns, ArrowUp: -columns, PageDown: columns * 3, PageUp: -columns * 3 }[event.key];
    const target = event.key === "Home" ? 0 : event.key === "End" ? ITEMS.length - 1 : step === undefined ? undefined : Math.max(0, Math.min(ITEMS.length - 1, active + step));
    if (target === undefined) return;
    event.preventDefault();
    moved.current = true;
    setActive(target);
    virtual.scrollToIndex(Math.floor(target / columns));
  };

  return (
    <div ref={scroller} className="max-h-100 overflow-y-auto overscroll-contain rounded-md">
      <div role="listbox" aria-label="Background scene" aria-disabled={disabled || undefined}
        onKeyDown={onKeyDown} className="relative w-full" style={{ height: virtual.getTotalSize() }}>
        {virtual.getVirtualItems().map((row) => (
          <div key={row.index} role="presentation" className="absolute inset-x-0 top-0 grid gap-2"
            style={{ transform: `translateY(${row.start}px)`, gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}>
            {ITEMS.slice(row.index * columns, row.index * columns + columns).map((item, k) => {
              const index = row.index * columns + k;
              const isSelected = selected === item.id;
              const src = item.id === NO_SCENE ? undefined : scenePreview(item.id);
              return (
                <Button key={item.id} variant="outline" role="option" aria-selected={isSelected} aria-posinset={index + 1} aria-setsize={ITEMS.length}
                  aria-disabled={disabled || undefined} data-index={index} data-scene={item.id} tabIndex={index === active ? 0 : -1}
                  onFocus={() => setActive(index)} onClick={() => { if (!disabled) onSelect(item.id); }}
                  className={cn("relative h-auto min-w-0 flex-col gap-0 overflow-hidden p-0 text-left sm:h-auto", isSelected && "border-primary ring-1 ring-primary", disabled && "opacity-64")}>
                  <span aria-hidden="true" className="flex w-full items-center justify-center overflow-hidden bg-muted text-muted-foreground" style={{ height: THUMB_H }}>
                    {src
                      ? <img src={src} alt="" width={100} height={50} loading="lazy" decoding="async" draggable={false} className="size-full object-cover [image-rendering:pixelated]" />
                      : <ImageOff className="size-5" />}
                  </span>
                  <span className="flex w-full items-center justify-between gap-2 px-2 text-xs" style={{ height: LABEL_H }}>
                    <span className="truncate">{item.label}</span>
                    {isSelected && <Badge size="sm">Selected</Badge>}
                  </span>
                </Button>
              );
            })}
          </div>
        ))}
      </div>
    </div>
  );
}
