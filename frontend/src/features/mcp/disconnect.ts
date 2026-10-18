import { useMemo } from "react";
import { create } from "zustand";
import { MCP, events, type ServerState } from "@/mygo";
import { toastManager } from "@/components/ui/toast";
import { inDesktop } from "@/lib/backend";
import { errorText } from "./shared";

/**
 * Servers the user disconnected. They leave every list at once instead of after the daemon answers, and
 * come back only if it refused. Ids are never reused, so a disconnected one is not un-hidden on success.
 */
export const useDisconnected = create<{ ids: ReadonlySet<string> }>(() => ({ ids: new Set() }));

export function useVisibleServers(servers: ServerState[]): ServerState[] {
  const gone = useDisconnected((state) => state.ids);
  return useMemo(() => (gone.size ? servers.filter((server) => !gone.has(server.id)) : servers), [servers, gone]);
}

/** The daemon finishes a removal in the background (bounded, about 20 s at most); stop listening after that. */
const CLEANUP_WINDOW_MS = 60_000;

/**
 * Warns when the daemon removed the server but could not finish closing it or deleting its credentials.
 * Listens only for the disconnect this window started, so a second open window does not repeat the toast.
 * Returns the unsubscribe.
 */
function watchCleanup(server: Pick<ServerState, "id" | "name">): () => void {
  if (!inDesktop) return () => {};
  const stop = () => { clearTimeout(timer); off(); };
  const off = events.mcpCleanupFailed.on((failure) => {
    if (failure.serverId !== server.id) return;
    stop();
    toastManager.add({ type: "warning", title: `${server.name} was disconnected, but cleanup is incomplete`, description: failure.error, timeout: 12000 });
  });
  const timer = setTimeout(stop, CLEANUP_WINDOW_MS);
  return stop;
}

/**
 * Disconnects without holding the caller: the server is hidden now and the daemon call runs on its own.
 * If the daemon refuses, the server comes back and a toast says why.
 */
export function disconnectServer(server: Pick<ServerState, "id" | "name">, reload: () => void): void {
  useDisconnected.setState((state) => ({ ids: new Set(state.ids).add(server.id) }));
  const stopWatching = watchCleanup(server);
  MCP.remove(server.id).then(reload, (cause: unknown) => {
    stopWatching();
    useDisconnected.setState((state) => {
      const ids = new Set(state.ids);
      ids.delete(server.id);
      return { ids };
    });
    reload();
    toastManager.add({ type: "error", title: `Could not disconnect ${server.name}`, description: errorText(cause), timeout: 8000 });
  });
}
