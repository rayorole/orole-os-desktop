import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ServerState } from "@/mygo";

const api = vi.hoisted(() => ({
  list: vi.fn(), add: vi.fn(), startAuth: vi.fn(), cancelAuth: vi.fn(), catalog: vi.fn(), diagnostics: vi.fn(), setToolPermission: vi.fn(), remove: vi.fn(), update: vi.fn(),
  github: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({ inDesktop: true, previewKey: "integrations-test" }));
vi.mock("@/mygo", () => {
  const event = { on: () => () => {} };
  return {
    MCP: { list: api.list, add: api.add, startAuth: api.startAuth, cancelAuth: api.cancelAuth, catalog: api.catalog, diagnostics: api.diagnostics, setToolPermission: api.setToolPermission, remove: api.remove, update: api.update },
    GitHub: { status: api.github },
    events: {
      mcpStatus: event, daemonState: event, githubChanged: event,
      mcpAuthDone: event,
    },
  };
});

import { Integrations } from "./Integrations";
import { DIRECTORY, nameFromUrl, serverFor } from "./directory";

const server = (patch: Partial<ServerState>): ServerState => ({
  id: "s1", name: "Vercel", url: "https://mcp.vercel.com", transport: "streamable-http", adapter: "", enabled: true,
  authKind: "oauth", oauthClientId: "", scopes: "", tokenHeader: "", createdAt: 0, status: "connected", authorized: true, toolCount: 3, ...patch,
});

beforeEach(() => {
  vi.clearAllMocks();
  api.list.mockResolvedValue([]);
  api.github.mockResolvedValue({ connected: false });
  api.catalog.mockResolvedValue({ tools: [] });
  api.diagnostics.mockResolvedValue({});
});
afterEach(cleanup);

describe("directory", () => {
  it("lists each endpoint once, over https unless it is a loopback desktop app", () => {
    const urls = DIRECTORY.flatMap((c) => c.url ?? []);
    expect(new Set(urls).size).toBe(urls.length);
    for (const c of DIRECTORY) {
      if (!c.url) continue;
      const parsed = new URL(c.url);
      if (c.local) expect(["127.0.0.1", "localhost"]).toContain(parsed.hostname);
      else expect(parsed.protocol).toBe("https:");
    }
  });

  it("matches a registered server to its entry regardless of a trailing slash or case", () => {
    const vercel = DIRECTORY.find((c) => c.id === "vercel")!;
    expect(serverFor(vercel, [server({ url: "https://MCP.vercel.com/" })])?.id).toBe("s1");
    expect(serverFor(vercel, [server({ url: "https://mcp.vercel.com/other" })])).toBeUndefined();
  });

  it("derives a name from the host", () => {
    expect(nameFromUrl("https://mcp.example.com/sse")).toBe("Example");
    expect(nameFromUrl("https://api.acme.io/mcp")).toBe("Acme");
    expect(nameFromUrl("not a url")).toBe("");
  });
});

describe("Integrations", () => {
  it("adds a directory entry with one click and starts sign-in when the server asks for it", async () => {
    api.add.mockResolvedValue(server({ status: "auth_required", authorized: false }));
    render(<Integrations />);
    fireEvent.click(await screen.findByRole("button", { name: "Add Vercel" }));
    await waitFor(() => expect(api.startAuth).toHaveBeenCalledExactlyOnceWith("s1"));
    expect(api.add).toHaveBeenCalledWith(expect.objectContaining({ name: "Vercel", url: "https://mcp.vercel.com", authKind: "oauth" }));
  });

  it("does not start sign-in for a server that connects without it", async () => {
    api.add.mockResolvedValue(server({ status: "connected" }));
    render(<Integrations />);
    fireEvent.click(await screen.findByRole("button", { name: "Add Cloudflare" }));
    await waitFor(() => expect(api.add).toHaveBeenCalled());
    expect(api.startAuth).not.toHaveBeenCalled();
  });

  it("starts sign-in for any server that is not connected once it settles, not only auth_required", async () => {
    api.add.mockResolvedValue(server({ status: "backoff", authorized: false }));
    render(<Integrations />);
    fireEvent.click(await screen.findByRole("button", { name: "Add Railway" }));
    await waitFor(() => expect(api.startAuth).toHaveBeenCalledExactlyOnceWith("s1"));
  });

  it("registers a local desktop-app connector without sign-in, even while the app is not running", async () => {
    api.add.mockResolvedValue(server({ id: "p1", name: "Paper", url: "http://127.0.0.1:29979/mcp", authKind: "none", status: "backoff", authorized: false }));
    render(<Integrations />);
    fireEvent.click(await screen.findByRole("button", { name: "Add Paper" }));
    await waitFor(() => expect(api.add).toHaveBeenCalledWith(expect.objectContaining({ name: "Paper", url: "http://127.0.0.1:29979/mcp", authKind: "none" })));
    expect(api.startAuth).not.toHaveBeenCalled();
  });

  it("asks for the user's own OAuth client for Google and always signs in, since the server never challenges", async () => {
    api.add.mockResolvedValue(server({ id: "g1", name: "Gmail", url: "https://gmailmcp.googleapis.com/mcp/v1", status: "connected" }));
    render(<Integrations />);
    fireEvent.click(await screen.findByRole("button", { name: "Add Gmail" }));
    expect(api.add).not.toHaveBeenCalled();
    fireEvent.change(await screen.findByLabelText("Client ID"), { target: { value: "abc.apps.googleusercontent.com" } });
    fireEvent.change(screen.getByLabelText("Client secret"), { target: { value: "s3cret" } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(api.startAuth).toHaveBeenCalledExactlyOnceWith("g1"));
    expect(api.add).toHaveBeenCalledWith(expect.objectContaining({
      url: "https://gmailmcp.googleapis.com/mcp/v1", authKind: "oauth", oauthClientId: "abc.apps.googleusercontent.com", clientSecret: "s3cret",
      scopes: expect.stringContaining("gmail.readonly"),
    }));
  });

  it("connects Similarweb with an API key sent in its api-key header, never browser sign-in", async () => {
    api.add.mockResolvedValue(server({ id: "w1", name: "Similarweb", url: "https://mcp.similarweb.com", authKind: "bearer", tokenHeader: "api-key", status: "connecting", authorized: true }));
    render(<Integrations />);
    fireEvent.click(await screen.findByRole("button", { name: "Add Similarweb" }));
    expect(api.add).not.toHaveBeenCalled();
    fireEvent.change(await screen.findByLabelText("API key"), { target: { value: " sw-key " } });
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(api.add).toHaveBeenCalledWith(expect.objectContaining({
      url: "https://mcp.similarweb.com", authKind: "bearer", bearerToken: "sw-key", tokenHeader: "api-key",
    })));
    expect(api.startAuth).not.toHaveBeenCalled();
  });

  it("moves a connected entry out of the directory and offers Manage", async () => {
    api.list.mockResolvedValue([server({})]);
    render(<Integrations />);
    const connected = await screen.findByRole("region", { name: "Connected" });
    expect(within(connected).getByText("Vercel")).toBeTruthy();
    expect(within(connected).getByRole("button", { name: "Manage" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Add Vercel" })).toBeNull();
  });

  it("adds a custom connector from just a name and URL, naming it from the host when blank", async () => {
    api.add.mockResolvedValue(server({ id: "c1", name: "Example", url: "https://mcp.example.com/mcp", status: "connected" }));
    render(<Integrations />);
    fireEvent.click(await screen.findByRole("button", { name: /Custom connector/ }));
    fireEvent.change(await screen.findByLabelText("Server URL"), { target: { value: "https://mcp.example.com/mcp" } });
    fireEvent.click(screen.getByRole("button", { name: "Add connector" }));
    await waitFor(() => expect(api.add).toHaveBeenCalledWith(expect.objectContaining({ name: "Example", url: "https://mcp.example.com/mcp" })));
  });

  it("adds a custom connector with an access token as a bearer server and never opens browser sign-in", async () => {
    api.add.mockResolvedValue(server({ id: "c2", name: "Canvas", url: "https://canvas.example.com/mcp", authKind: "bearer", status: "connecting", authorized: false }));
    render(<Integrations />);
    fireEvent.click(await screen.findByRole("button", { name: /Custom connector/ }));
    fireEvent.change(await screen.findByLabelText("Server URL"), { target: { value: "https://canvas.example.com/mcp" } });
    fireEvent.change(screen.getByLabelText("Access token (optional)"), { target: { value: " tok-123 " } });
    fireEvent.click(screen.getByRole("button", { name: "Add connector" }));
    await waitFor(() => expect(api.add).toHaveBeenCalledWith(expect.objectContaining({ url: "https://canvas.example.com/mcp", authKind: "bearer", bearerToken: "tok-123" })));
    expect(api.startAuth).not.toHaveBeenCalled();
  });

  it("replaces a rejected bearer token from Manage instead of offering browser sign-in", async () => {
    api.list.mockResolvedValue([server({ authKind: "bearer", status: "auth_required", authorized: false })]);
    api.update.mockResolvedValue(server({ authKind: "bearer" }));
    render(<Integrations />);
    await screen.findByRole("button", { name: "Manage" });
    expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Manage" }));
    expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
    fireEvent.change(await screen.findByLabelText("Access token"), { target: { value: "new-token" } });
    fireEvent.click(screen.getByRole("button", { name: "Replace token" }));
    await waitFor(() => expect(api.update).toHaveBeenCalledWith("s1", { bearerToken: "new-token" }));
    expect(api.startAuth).not.toHaveBeenCalled();
  });

  it("shows a failed add instead of swallowing it", async () => {
    api.add.mockRejectedValue(new Error("a server with this URL is already registered"));
    render(<Integrations />);
    fireEvent.click(await screen.findByRole("button", { name: "Add Vercel" }));
    expect(await screen.findByText(/already registered/)).toBeTruthy();
  });

  describe("details page", () => {
    const tools = [
      { name: "list_projects", description: "", permission: "ask" },
      { name: "get_deployment", description: "", permission: "allow" },
      { name: "delete_project", description: "", permission: "deny" },
    ];
    async function open() {
      api.list.mockResolvedValue([server({})]);
      api.catalog.mockResolvedValue({ tools });
      render(<Integrations />);
      fireEvent.click(await screen.findByRole("button", { name: "Manage" }));
    }

    it("shows the tools as chips, reauthenticate instead of connect, and keeps OAuth client settings collapsed", async () => {
      await open();
      expect(await screen.findByText("2 of 3 tools enabled")).toBeTruthy();
      expect(screen.getByRole("button", { name: "list_projects" }).getAttribute("aria-pressed")).toBe("true");
      expect(screen.getByRole("button", { name: "delete_project" }).getAttribute("aria-pressed")).toBe("false");
      expect(screen.getByRole("button", { name: "Reauthenticate" })).toBeTruthy();
      expect(screen.queryByRole("button", { name: "Connect" })).toBeNull();
      expect(screen.getByText("https://mcp.vercel.com")).toBeTruthy();
      expect(screen.getByText("OAuth client").closest("details")!.open).toBe(false);
    });

    it("turns a tool off from its chip", async () => {
      await open();
      fireEvent.click(await screen.findByRole("button", { name: "list_projects" }));
      await waitFor(() => expect(api.setToolPermission).toHaveBeenCalledWith("s1", "list_projects", "deny"));
    });

    it("offers Sign in, not Reauthenticate, while the server is not signed in", async () => {
      api.list.mockResolvedValue([server({ status: "auth_required", authorized: false })]);
      render(<Integrations />);
      fireEvent.click(await screen.findByRole("button", { name: "Manage" }));
      expect(await screen.findByRole("button", { name: "Sign in" })).toBeTruthy();
      expect(screen.queryByRole("button", { name: "Reauthenticate" })).toBeNull();
    });

    it("disconnects after confirmation and returns to the list", async () => {
      await open();
      fireEvent.click(await screen.findByRole("button", { name: "Disconnect" }));
      const dialog = await screen.findByRole("dialog");
      fireEvent.click(within(dialog).getByRole("button", { name: "Disconnect" }));
      await waitFor(() => expect(api.remove).toHaveBeenCalledWith("s1"));
      expect(await screen.findByRole("button", { name: "Add Notion" })).toBeTruthy();
    });
  });
});
