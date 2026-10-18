import { useRef, useState, type DragEvent } from "react";
import { dragHasFiles } from "./attachmentFiles";

interface Options {
  /** False leaves every drag alone (the composer takes no files at all). */
  enabled: boolean;
  /** Whether a drop would be taken now; a composer that is full or busy still shows that it refuses. */
  accepting: boolean;
  onFiles: (files: File[]) => void;
  /** Runs after any file drop, taken or not (the composer returns focus to its field). */
  afterDrop?: () => void;
}

/** Drag-and-drop of files onto a composer, shared by the composers that build their own surface. Only file drags are taken over. */
export function useFileDrop({ enabled, accepting, onFiles, afterDrop }: Options) {
  const depth = useRef(0);
  const [dragging, setDragging] = useState(false);
  return {
    dragging,
    handlers: {
      onDragEnter(event: DragEvent) {
        if (!enabled || !dragHasFiles(event.dataTransfer)) return;
        depth.current++;
        setDragging(true);
      },
      onDragOver(event: DragEvent) {
        if (!enabled || !dragHasFiles(event.dataTransfer)) return;
        event.preventDefault();
        event.dataTransfer.dropEffect = accepting ? "copy" : "none";
      },
      onDragLeave(event: DragEvent) {
        if (!dragHasFiles(event.dataTransfer)) return;
        depth.current = Math.max(0, depth.current - 1);
        if (depth.current === 0) setDragging(false);
      },
      onDrop(event: DragEvent) {
        if (!enabled || !dragHasFiles(event.dataTransfer)) return;
        event.preventDefault();
        depth.current = 0;
        setDragging(false);
        const files = Array.from(event.dataTransfer.files);
        if (accepting && files.length) onFiles(files);
        afterDrop?.();
      },
    },
  };
}
