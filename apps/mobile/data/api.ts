/**
 * Mobile-owned fetch wrapper. Mirrors the surface area of
 * packages/core/api/client.ts that mobile actually uses, but lives in
 * apps/mobile/ so we control retry/timeout/error handling independently.
 *
 * Types are imported via `import type` from @multica/core/types — zero
 * runtime coupling. Zod schemas + fallbacks are imported from
 * @multica/core/api/schemas (pure data, on the mobile sharing whitelist).
 *
 * Design checklist (apps/mobile/CLAUDE.md "Lessons → ApiClient capability list"):
 *   1. Zod parseWithFallback for endpoints with schemas (drift defense)
 *   2. onUnauthorized callback on 401 (auto sign-out, avoids retry loops)
 *   3. X-Request-ID per request + structured logger (debug + tracing)
 *   4. Bearer auth + X-Workspace-Slug — NOT cookie auth (no CSRF, no credentials)
 */
import type {
  Agent,
  Comment,
  InboxItem,
  Issue,
  ListIssuesParams,
  ListIssuesResponse,
  MemberWithUser,
  TimelinePage,
  User,
  Workspace,
} from "@multica/core/types";
import {
  EMPTY_LIST_ISSUES_RESPONSE,
  EMPTY_TIMELINE_PAGE,
  ListIssuesResponseSchema,
  TimelinePageSchema,
} from "@multica/core/api/schemas";
import { getCurrentSlug } from "./workspace-store";
import { parseWithFallback } from "@/lib/parse-response";
import { createRequestId } from "@/lib/request-id";

const API_URL = process.env.EXPO_PUBLIC_API_URL;

if (!API_URL) {
  throw new Error(
    "EXPO_PUBLIC_API_URL is not set. Add it to apps/mobile/.env.development.local " +
      "(see apps/mobile/.env.staging for an example).",
  );
}

export interface LoginResponse {
  token: string;
  user: User;
}

export class ApiError extends Error {
  readonly status: number;
  readonly body?: unknown;
  constructor(message: string, status: number, body?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }
}

export interface ApiClientOptions {
  /** Called once when the server returns 401. The platform layer wires this
   *  to clear the token + navigate to /login so a stale token doesn't keep
   *  every subsequent request looping on 401. */
  onUnauthorized?: () => void;
}

class ApiClient {
  private token: string | null = null;
  private options: ApiClientOptions = {};

  setToken(token: string | null) {
    this.token = token;
  }

  setOptions(options: ApiClientOptions) {
    this.options = { ...this.options, ...options };
  }

  private async fetch<T>(path: string, init: RequestInit = {}): Promise<T> {
    const rid = createRequestId();
    const start = Date.now();
    const method = init.method ?? "GET";

    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      "X-Client-Platform": "mobile",
      "X-Client-OS": "ios",
      "X-Client-Version": "0.1.0",
      "X-Request-ID": rid,
      ...((init.headers as Record<string, string>) ?? {}),
    };
    if (this.token) {
      headers["Authorization"] = `Bearer ${this.token}`;
    }
    // Backend middleware (server/internal/middleware/workspace.go) resolves
    // slug → ws UUID and gates membership. Mirrors packages/core/api/client.ts.
    const slug = getCurrentSlug();
    if (slug) {
      headers["X-Workspace-Slug"] = slug;
    }

    console.log(`[api] → ${method} ${path}`, { rid });

    const res = await fetch(`${API_URL}${path}`, { ...init, headers });
    const duration = Date.now() - start;

    if (!res.ok) {
      // 401 sign-out hook: invoke once, let the platform layer (auth-store)
      // clear the token + navigate. Subsequent requests in flight will also
      // 401 and re-enter here, so the callback must be idempotent.
      if (res.status === 401) {
        this.options.onUnauthorized?.();
      }

      let body: unknown;
      try {
        body = await res.json();
      } catch {
        body = undefined;
      }
      const message =
        (body && typeof body === "object" && "message" in body
          ? String((body as { message: unknown }).message)
          : null) ?? `${res.status} ${res.statusText}`;

      const level = res.status === 404 ? "warn" : "error";
      console[level](`[api] ← ${res.status} ${path}`, {
        rid,
        duration: `${duration}ms`,
        error: message,
      });

      throw new ApiError(message, res.status, body);
    }

    console.log(`[api] ← ${res.status} ${path}`, {
      rid,
      duration: `${duration}ms`,
    });

    if (res.status === 204) return undefined as T;
    return (await res.json()) as T;
  }

  // --- Auth ---
  async sendCode(email: string): Promise<void> {
    await this.fetch<void>("/auth/send-code", {
      method: "POST",
      body: JSON.stringify({ email }),
    });
  }

  async verifyCode(email: string, code: string): Promise<LoginResponse> {
    return this.fetch<LoginResponse>("/auth/verify-code", {
      method: "POST",
      body: JSON.stringify({ email, code }),
    });
  }

  async getMe(): Promise<User> {
    return this.fetch<User>("/api/me");
  }

  // --- Workspaces ---
  async listWorkspaces(): Promise<Workspace[]> {
    return this.fetch<Workspace[]>("/api/workspaces");
  }

  // --- Inbox ---
  async listInbox(): Promise<InboxItem[]> {
    return this.fetch<InboxItem[]>("/api/inbox");
  }

  async markInboxRead(id: string): Promise<InboxItem> {
    return this.fetch<InboxItem>(`/api/inbox/${id}/read`, { method: "POST" });
  }

  // --- Members & Agents (for actor name/avatar lookup) ---
  async listMembers(workspaceId: string): Promise<MemberWithUser[]> {
    return this.fetch<MemberWithUser[]>(
      `/api/workspaces/${workspaceId}/members`,
    );
  }

  async listAgents(): Promise<Agent[]> {
    return this.fetch<Agent[]>("/api/agents");
  }

  // --- Issues ---
  async listIssues(params: ListIssuesParams = {}): Promise<ListIssuesResponse> {
    const search = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v == null) continue;
      if (Array.isArray(v)) {
        for (const item of v) search.append(k, String(item));
      } else {
        search.set(k, String(v));
      }
    }
    const qs = search.toString();
    const raw = await this.fetch<unknown>(
      `/api/issues${qs ? `?${qs}` : ""}`,
    );
    return parseWithFallback(raw, ListIssuesResponseSchema, EMPTY_LIST_ISSUES_RESPONSE, {
      endpoint: "GET /api/issues",
    });
  }

  async getIssue(id: string): Promise<Issue> {
    return this.fetch<Issue>(`/api/issues/${id}`);
  }

  // V1 only walks "latest → before" (oldest direction). `after` / `around`
  // are not yet exposed because mobile v1 has no WS push and no notification
  // deep-link landing target. Mirror of packages/core/api/client.ts
  // restricted to that subset.
  async listTimeline(
    issueId: string,
    cursor?: { mode: "before"; cursor: string } | null,
    limit = 50,
  ): Promise<TimelinePage> {
    const p = new URLSearchParams();
    p.set("limit", String(limit));
    if (cursor?.mode === "before") p.set("before", cursor.cursor);
    const raw = await this.fetch<unknown>(
      `/api/issues/${issueId}/timeline?${p.toString()}`,
    );
    return parseWithFallback(raw, TimelinePageSchema, EMPTY_TIMELINE_PAGE, {
      endpoint: "GET /api/issues/:id/timeline",
    });
  }

  async createComment(issueId: string, content: string): Promise<Comment> {
    return this.fetch<Comment>(`/api/issues/${issueId}/comments`, {
      method: "POST",
      body: JSON.stringify({ content }),
    });
  }
}

export const api = new ApiClient();
