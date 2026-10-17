import { useState } from "react";
import { MCP } from "@/mygo";
import { errorText } from "@/features/mcp/shared";

export interface ConnectOptions {
  /** Loopback desktop-app server: registered without auth, no sign-in. */
  local?: boolean;
  /** User-supplied OAuth client (Google). The secret goes to the credential store, never back to the UI. */
  client?: { id: string; secret: string; scopes: string };
  /** Static token: stored in the credential store, no browser sign-in. */
  token?: string;
  /** Header that carries the token (an API key such as Similarweb's "api-key"); default Authorization: Bearer. */
  tokenHeader?: string;
}

/**
 * Registers an MCP server and signs in to it. Once the first connection attempt has settled, any
 * server that is not connected gets authorization started straight away (the shell opens the
 * browser at the vendor's consent page); a server that needs no sign-in just ends up connected.
 * A server with a user-supplied client always signs in: it answers unauthenticated requests, so
 * "connected" does not mean authorized. A server added with a static token never signs in: a
 * rejected token shows up as "needs sign-in" and is replaced from the details page.
 * Registration is kept when sign-in fails so the error stays inspectable on the details page.
 */
export async function connectServer(name: string, url: string, options: ConnectOptions = {}): Promise<{ id: string; signingIn: boolean }> {
  const { local, client, token, tokenHeader } = options;
  const added = await MCP.add({
    name, url, adapter: "", authKind: local ? "none" : token ? "bearer" : "oauth",
    oauthClientId: client?.id ?? "", scopes: client?.scopes ?? "",
    ...(client?.secret ? { clientSecret: client.secret } : {}), ...(token ? { bearerToken: token } : {}),
    ...(token && tokenHeader ? { tokenHeader } : {}),
  });
  if (local || token) return { id: added.id, signingIn: false };
  let state = added;
  for (let attempt = 0; attempt < 25 && state.status === "connecting"; attempt++) {
    const { promise, resolve } = Promise.withResolvers<void>();
    setTimeout(resolve, 400);
    await promise;
    state = (await MCP.list()).find((server) => server.id === added.id) ?? state;
  }
  const signingIn = client !== undefined || state.status !== "connected";
  if (signingIn) await MCP.startAuth(added.id);
  return { id: added.id, signingIn };
}

/** Busy and error state for one-click connects, keyed by whatever the caller uses to name a row. */
export function useConnect(changed: () => void) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  async function run(key: string, action: () => Promise<unknown>): Promise<boolean> {
    setBusy(key); setError(null);
    try { await action(); return true; }
    catch (cause) { setError(errorText(cause)); return false; }
    finally { setBusy(null); changed(); }
  }
  return { busy, error, clearError: () => setError(null), run };
}
