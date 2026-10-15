// The parts of noVNC 1.7.0's RFB (core/rfb.js, docs/API.md) Orole uses. noVNC ships no TypeScript types.
declare module "@novnc/novnc" {
  export interface RFBChannel {
    binaryType: string;
    protocol: string;
    readyState: string | number;
    send(data: ArrayBuffer | Uint8Array): void;
    close(): void;
    onopen: unknown;
    onmessage: unknown;
    onerror: unknown;
    onclose: unknown;
  }
  export interface RFBOptions {
    shared?: boolean;
    credentials?: { username?: string; password?: string; target?: string };
    repeaterID?: string;
    wsProtocols?: string[];
  }
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, urlOrChannel: string | RFBChannel, options?: RFBOptions);
    viewOnly: boolean;
    scaleViewport: boolean;
    clipViewport: boolean;
    resizeSession: boolean;
    focusOnClick: boolean;
    showDotCursor: boolean;
    background: string;
    qualityLevel: number;
    compressionLevel: number;
    disconnect(): void;
    focus(options?: FocusOptions): void;
    blur(): void;
  }
}
