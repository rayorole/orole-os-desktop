import { useEffect, useRef, useState } from "react";
import RFB from "@novnc/novnc";
import { Button } from "@/components/ui/button";
import { errorText } from "./computerState";
import { ScreenChannel } from "./screenChannel";

/**
 * The bot computer's screen, drawn by noVNC from the daemon's screen stream. View-only unless the user has taken
 * over; even then the daemon, not this flag, decides whether keyboard and mouse reach the computer.
 */
export function LiveScreen({ botId, interactive }: { botId: string; interactive: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  const rfbRef = useRef<RFB | null>(null);
  const interactiveRef = useRef(interactive);
  const [state, setState] = useState<"connecting" | "connected" | "disconnected">("connecting");
  const [reason, setReason] = useState("");
  const [attempt, setAttempt] = useState(0);
  interactiveRef.current = interactive;

  useEffect(() => {
    setState("connecting");
    setReason("");
    const channel = new ScreenChannel(botId);
    const rfb = new RFB(host.current!, channel, { shared: true });
    rfb.viewOnly = !interactiveRef.current;
    rfb.scaleViewport = true;
    rfb.resizeSession = false;
    // A replaced RFB (reconnect, bot switch) may still fire; only the current one may change what is shown.
    rfb.addEventListener("connect", () => { if (rfbRef.current === rfb) setState("connected"); });
    rfb.addEventListener("disconnect", () => {
      if (rfbRef.current !== rfb) return;
      setReason(errorText(channel.closeReason));
      setState("disconnected");
    });
    rfbRef.current = rfb;
    void channel.open();
    return () => {
      rfbRef.current = null;
      try { rfb.disconnect(); } catch { /* already disconnected */ }
      channel.close();
    };
  }, [botId, attempt]);

  // Keyboard focus follows the takeover: the canvas takes it when the user gets control and gives it up on release.
  // Before the screen connects there is nothing to focus, so this runs again on connect.
  useEffect(() => {
    const rfb = rfbRef.current;
    if (!rfb) return;
    rfb.viewOnly = !interactive;
    if (state !== "connected") return;
    if (interactive) rfb.focus(); else rfb.blur();
  }, [interactive, state]);

  return (
    <div role="region" aria-label="Live screen" data-slot="live-screen"
      className="relative flex min-h-48 flex-[3] overflow-hidden rounded-lg border bg-muted/40">
      <div ref={host} className="size-full" />
      {state !== "connected" && (
        <div role="status" className="absolute inset-0 flex flex-col items-center justify-center gap-2 text-sm text-muted-foreground">
          {state === "connecting" ? "Connecting to the screen…" : reason ? `The screen disconnected: ${reason}` : "The screen disconnected."}
          {state === "disconnected" && <Button type="button" size="xs" variant="outline" onClick={() => setAttempt((a) => a + 1)}>Reconnect</Button>}
        </div>
      )}
    </div>
  );
}
