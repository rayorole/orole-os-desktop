/*
 * Shared state for the Prompt Input block, adapted from HextaUI (https://hextaui.com/blocks/prompt-input).
 * See docs/research/upstream-attribution.md.
 */
import { createContext, useContext, type RefObject } from "react";

export interface PromptInputContextValue {
  value: string;
  setValue: (value: string) => void;
  textareaRef: RefObject<HTMLTextAreaElement | null>;
  disabled: boolean;
  canSubmit: boolean;
  dragging: boolean;
  /** Undefined when the composer takes no files. */
  addFiles?: (files: File[]) => void;
  /** Takes pasted files even while the composer is full, so the caller can say why they were refused. Undefined when it takes no files. */
  receiveFiles?: (files: File[]) => void;
  openFileDialog: () => void;
}

export const PromptInputContext = createContext<PromptInputContextValue | null>(null);

export function usePromptInput(part: string): PromptInputContextValue {
  const context = useContext(PromptInputContext);
  if (!context) throw new Error(`${part} must be used within <PromptInput>.`);
  return context;
}

export function prefersReducedMotion(): boolean {
  return typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

