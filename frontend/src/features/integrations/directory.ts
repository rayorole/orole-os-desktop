import type { Logo } from "./BrandLogo";
import type { ServerState } from "@/mygo";

export const CATEGORIES = ["Developer", "Productivity", "Finance", "Analytics", "Design"] as const;
export type Category = (typeof CATEGORIES)[number];

export interface Connector {
  id: string;
  name: string;
  description: string;
  /** A sentence or two for the details page. */
  about: string;
  category: Category;
  /** The company's official logo, found in ./logos by id; entries without a file get the plug. */
  logo?: Logo;
  /** Shown first, before the categories. */
  featured?: boolean;
  /** Remote MCP endpoint (Streamable HTTP, OAuth). GitHub has none: it uses the local gh CLI integration. */
  url?: string;
  /**
   * Served by a desktop app on this machine over loopback HTTP, with no sign-in. Adding it only
   * registers the endpoint; it connects whenever that app is running.
   */
  local?: boolean;
  /**
   * The server does not register OAuth clients itself (Google): the user supplies their own client,
   * and these scopes are requested at sign-in. The server answers unauthenticated requests, so
   * sign-in always starts right after adding.
   */
  client?: { scopes: string; help: string };
  /**
   * The server takes an API key in this header instead of browser sign-in (Similarweb). The key goes
   * to the credential store; no sign-in starts.
   */
  apiKey?: { header: string; help: string };
  native?: "github";
}

/**
 * Official remote MCP servers. Each URL was checked against the live server: it answers 401 with
 * RFC 9728 protected-resource metadata and its authorization server accepts dynamic client
 * registration, so Orole's OAuth flow can connect with nothing more than a click.
 * Servers that need a pre-registered client (Slack, Render, Asana, ...) are left to "Custom connector".
 * Similarweb is the exception with an API key: its OAuth server sits behind a firewall that refuses every
 * request naming a loopback redirect (http://127.0.0.1/...), which a desktop app's sign-in needs.
 */
const ENTRIES: Omit<Connector, "logo">[] = [
  { id: "github", name: "GitHub", description: "Search repos and PRs", about: "Search repositories and code, read issues and pull requests, check CI, and work with your notifications through the GitHub CLI you are already signed in with.", category: "Developer", featured: true, native: "github" },
  { id: "vercel", name: "Vercel", description: "Manage your deploys", about: "Manage Vercel projects and deployments: list and inspect deployments, read build and runtime logs, work with domains, DNS and environment variables, and search the Vercel documentation.", category: "Developer", featured: true, url: "https://mcp.vercel.com" },
  { id: "cloudflare", name: "Cloudflare", description: "Workers, Pages, DNS and more", about: "Work with your Cloudflare account through the Cloudflare API: Workers, Pages, DNS, storage and more.", category: "Developer", featured: true, url: "https://mcp.cloudflare.com/mcp" },
  { id: "notion", name: "Notion", description: "Search pages and docs", about: "Search your Notion workspace, read pages and databases, and create or update content.", category: "Productivity", featured: true, url: "https://mcp.notion.com/mcp" },
  { id: "linear", name: "Linear", description: "Track issues and projects", about: "Find, create and update Linear issues, projects and comments.", category: "Productivity", featured: true, url: "https://mcp.linear.app/mcp" },
  { id: "stripe", name: "Stripe", description: "Look up payments", about: "Look up customers, payments, subscriptions and invoices in your Stripe account.", category: "Finance", featured: true, url: "https://mcp.stripe.com" },

  { id: "netlify", name: "Netlify", description: "Manage your deploys", about: "Manage Netlify sites and deploys, and their configuration.", category: "Developer", url: "https://mcp.netlify.com/mcp" },
  { id: "railway", name: "Railway", description: "Manage services and environments", about: "Manage Railway projects: services, deployments, variables, domains and volumes, plus logs, metrics and the Railway documentation.", category: "Developer", url: "https://mcp.railway.com" },
  { id: "supabase", name: "Supabase", description: "Databases, auth and storage", about: "Manage Supabase projects: query and migrate databases, and work with project settings.", category: "Developer", url: "https://mcp.supabase.com/mcp" },
  { id: "neon", name: "Neon", description: "Manage Postgres databases", about: "Manage Neon Postgres projects and branches, and run SQL against them.", category: "Developer", url: "https://mcp.neon.tech/mcp" },
  { id: "prisma", name: "Prisma", description: "Manage Prisma Postgres", about: "Manage Prisma Postgres databases and run schema and migration tasks.", category: "Developer", url: "https://mcp.prisma.io/mcp" },
  { id: "sentry", name: "Sentry", description: "Triage errors and issues", about: "Search and triage Sentry issues, errors and events across your projects.", category: "Developer", url: "https://mcp.sentry.dev/mcp" },

  { id: "zapier", name: "Zapier", description: "Run actions across 8,000 apps", about: "Run the Zapier actions you have enabled across thousands of connected apps.", category: "Productivity", url: "https://mcp.zapier.com/api/mcp/mcp" },
  { id: "composio", name: "Composio", description: "Use 1,000+ apps through one connection", about: "Composio Connect gives your bots tools for more than a thousand apps, such as Gmail, Slack, GitHub and Notion, through one connection. You sign in to Composio once, then link each app inside Composio when a tool first needs it.", category: "Productivity", url: "https://connect.composio.dev/mcp" },
  { id: "intercom", name: "Intercom", description: "Search conversations and contacts", about: "Search Intercom conversations and contacts for support context.", category: "Productivity", url: "https://mcp.intercom.com/mcp" },

  { id: "paypal", name: "PayPal", description: "Invoices, orders and payments", about: "Work with PayPal invoices, orders and payments.", category: "Finance", url: "https://mcp.paypal.com/mcp" },
  { id: "posthog", name: "PostHog", description: "Product analytics and feature flags", about: "Query PostHog insights, events and persons, and manage feature flags, experiments and dashboards for your projects.", category: "Analytics", url: "https://mcp.posthog.com/mcp" },
  { id: "resend", name: "Resend", description: "Send and manage email", about: "Send email and manage Resend domains, audiences, contacts and broadcasts.", category: "Developer", url: "https://mcp.resend.com/mcp" },
  { id: "firecrawl", name: "Firecrawl", description: "Scrape, search and crawl the web", about: "Scrape pages into clean markdown, search the web, crawl and map sites, and extract structured data with Firecrawl.", category: "Developer", url: "https://mcp.firecrawl.dev/v2/mcp-oauth" },
  { id: "paper", name: "Paper", description: "Read and edit your Paper designs", about: "Read and write the file open in Paper Desktop: generate layouts, pull in real content, or turn a selected frame into code. Paper Desktop must be running with a file open; the connection is local to this machine.", category: "Design", url: "http://127.0.0.1:29979/mcp", local: true },
  { id: "gmail", name: "Gmail", description: "Search, read and draft email", about: "Search and read Gmail messages and threads, manage labels, and create drafts. Google does not register clients automatically: you create an OAuth client in your own Google Cloud project, and Orole asks only for the scopes listed in the setup step.", category: "Productivity", url: "https://gmailmcp.googleapis.com/mcp/v1", client: { scopes: "https://www.googleapis.com/auth/gmail.readonly https://www.googleapis.com/auth/gmail.compose", help: "Enable the Gmail API and the Gmail MCP service in a Google Cloud project, then create an OAuth client of type Desktop app." } },
  { id: "google-drive", name: "Google Drive", description: "Search and read your files", about: "Search Google Drive, read file contents and metadata, and create or copy files. Needs an OAuth client from your own Google Cloud project.", category: "Productivity", url: "https://drivemcp.googleapis.com/mcp/v1", client: { scopes: "https://www.googleapis.com/auth/drive.readonly https://www.googleapis.com/auth/drive.file", help: "Enable the Google Drive API and the Drive MCP service in a Google Cloud project, then create an OAuth client of type Desktop app." } },
  { id: "google-calendar", name: "Google Calendar", description: "Check and manage your schedule", about: "List calendars and events, check free/busy time, and create or update events. Needs an OAuth client from your own Google Cloud project.", category: "Productivity", url: "https://calendarmcp.googleapis.com/mcp/v1", client: { scopes: "https://www.googleapis.com/auth/calendar.readonly https://www.googleapis.com/auth/calendar.events", help: "Enable the Google Calendar API and the Calendar MCP service in a Google Cloud project, then create an OAuth client of type Desktop app." } },
  { id: "clay", name: "Clay", description: "Enrich contacts and companies", about: "Search and enrich people and companies with Clay, and work with your Clay tables.", category: "Productivity", url: "https://api.clay.com/v3/mcp" },
  { id: "apollo", name: "Apollo", description: "Prospect people and companies", about: "Search and enrich people and organizations in Apollo.io for sales prospecting.", category: "Productivity", url: "https://mcp.apollo.io/mcp" },
  { id: "figma", name: "Figma", description: "Read your designs", about: "Read Figma designs and their structure, variables and components to turn them into code.", category: "Design", url: "https://mcp.figma.com/mcp" },
  { id: "mobbin", name: "Mobbin", description: "Search UI design references", about: "Search Mobbin's library of real app screens, flows and website sections for design reference.", category: "Design", url: "https://api.mobbin.com/mcp" },
  { id: "similarweb", name: "Similarweb", description: "Website and market traffic data", about: "Look up website traffic, audience, competitors and market data from Similarweb.", category: "Analytics", url: "https://mcp.similarweb.com",
    apiKey: { header: "api-key", help: "Similarweb connects with an API key from a plan with API access (API-only, Business or Enterprise). An account admin creates one at pro.similarweb.com under Settings › Account › API, then turns it on in the Activation column." } },

  { id: "mixpanel", name: "Mixpanel", description: "Query product analytics", about: "Query Mixpanel events, funnels and retention for your projects.", category: "Analytics", url: "https://mcp.mixpanel.com/mcp" },
];

const LOGO_FILES = import.meta.glob<string>("./logos/*", { eager: true, query: "?url", import: "default" });
/** Single dark marks, painted in the foreground colour (see Logo.mono). */
const MONO = new Set(["github", "vercel", "linear", "railway", "resend", "prisma", "intercom", "composio"]);

export const DIRECTORY: Connector[] = ENTRIES.map((entry) => {
  const src = Object.entries(LOGO_FILES).find(([path]) => path.replace(/^.*\/|\.\w+$/g, "") === entry.id)?.[1];
  return src ? { ...entry, logo: { src, title: entry.name, mono: MONO.has(entry.id) } } : entry;
});

/** The registered server that belongs to a directory entry, if any. */
export function serverFor(connector: Connector, servers: ServerState[]): ServerState | undefined {
  if (!connector.url) return undefined;
  const key = (url: string) => url.trim().replace(/\/+$/, "").toLowerCase();
  const want = key(connector.url);
  return servers.find((server) => key(server.url) === want);
}

/** "https://mcp.example.com/sse" -> "Example": a readable default for a custom connector's name. */
export function nameFromUrl(raw: string): string {
  let host: string;
  try { host = new URL(raw.trim()).hostname; } catch { return ""; }
  const labels = host.split(".").filter((label) => !["www", "mcp", "api"].includes(label));
  const label = (labels.length > 1 ? labels[labels.length - 2] : labels[0]) ?? host;
  return label.charAt(0).toUpperCase() + label.slice(1);
}
