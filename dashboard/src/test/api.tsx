import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { vi } from "vitest";
import type { JobDetail, Me, Repo } from "../api/types";

type Handler = (body: unknown, url: URL) => { status?: number; body?: unknown };

export interface Call {
  method: string;
  path: string;
  body: unknown;
}

/** Routes `fetch` calls to handlers keyed by "METHOD /path" and records them. */
export function mockApi(routes: Record<string, Handler | object>): Call[] {
  const calls: Call[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, "http://test");
    const method = (init?.method ?? "GET").toUpperCase();
    const path = url.pathname.replace(/^\/api/, "");
    const body: unknown = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
    calls.push({ method, path, body });
    const route = routes[`${method} ${path}`];
    if (route === undefined) {
      return Promise.resolve(json(404, { detail: `no mock for ${method} ${path}` }));
    }
    const result = typeof route === "function" ? (route as Handler)(body, url) : { body: route };
    return Promise.resolve(json(result.status ?? 200, result.body ?? null));
  });
  return calls;
}

/** A route that answers with a specific status code. */
export function reply(status: number, body?: unknown): Handler {
  return () => ({ status, body });
}

function json(status: number, body: unknown): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

export function renderAt(path: string, pattern: string, element: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path={pattern} element={element} />
          <Route path="*" element={<p>elsewhere</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

export const ME: Me = {
  user: { id: "u1", login: "demo", avatar_url: null },
  auth_mode: "dev",
  github_connected: false,
};

export const REPO: Repo = {
  id: "r1",
  full_name: "demo/datekit",
  clone_url: "file:///sample-repos/datekit.git",
  default_branch: "main",
  config: {},
  is_github: false,
  created_at: "2026-10-09T10:00:00Z",
  latest_snapshot: {
    id: "s1",
    commit_sha: "5715566dd154404e8f6ebc082f3c5e39",
    status: "ready",
    file_count: 7,
    chunk_count: 31,
    embedding_model: "local-hash-v1",
    error: null,
    created_at: "2026-10-09T10:00:05Z",
    finished_at: "2026-10-09T10:00:07Z",
  },
};

const DIFF = `diff --git a/datekit/calendar.py b/datekit/calendar.py
--- a/datekit/calendar.py
+++ b/datekit/calendar.py
@@ -11,1 +11,1 @@
-    return year % 4 == 0
+    return year % 4 == 0 and (year % 100 != 0 or year % 400 == 0)
`;

const step = (id: string, sequence: number, iteration: number, agent: JobDetail["steps"][number]["agent"]) => ({
  id,
  sequence,
  iteration,
  agent,
  status: "succeeded" as const,
  model: "mock",
  input_tokens: 1200,
  output_tokens: 300,
  cost_usd: "0.0123",
  latency_ms: 850,
  error: null,
  started_at: "2026-10-09T10:01:00Z",
  finished_at: "2026-10-09T10:01:01Z",
});

const check = (status: "passed" | "failed", summary: string) => ({ status, summary, tool: "pytest", findings: [] });

export const JOB: JobDetail = {
  id: "j1",
  repo_id: "r1",
  repo_name: "demo/datekit",
  task: "Fix the failing test in datekit/calendar.py",
  status: "awaiting_review",
  current_iteration: 2,
  max_iterations: 3,
  total_cost_usd: "0.0492",
  created_at: "2026-10-09T10:01:00Z",
  completed_at: "2026-10-09T10:02:00Z",
  pr_url: null,
  repo_is_github: false,
  base_commit_sha: "5715566dd154404e8f6ebc082f3c5e39",
  plan: { summary: "Correct the century rule", steps: ["Edit is_leap_year"], risks: ["Off-by-one"] },
  review: {
    summary: "Fixes leap years for century years.",
    risk_level: "low",
    concerns: ["Check 1900"],
    suggested_followups: [],
    ready_to_merge: true,
  },
  review_feedback: null,
  error: null,
  total_input_tokens: 4800,
  total_output_tokens: 1200,
  steps: [
    step("st1", 1, 1, "planner"),
    step("st2", 2, 1, "coder"),
    step("st3", 3, 1, "tester"),
    step("st4", 4, 2, "debugger"),
    step("st5", 5, 2, "reviewer"),
  ],
  patches: [
    {
      id: "p1",
      iteration: 1,
      status: "superseded",
      diff: DIFF,
      files_changed: 1,
      additions: 1,
      deletions: 1,
      created_at: "2026-10-09T10:01:10Z",
      validations: [
        {
          id: "v1",
          status: "failed",
          tests_status: "failed",
          security_status: "passed",
          static_status: "passed",
          tests: check("failed", "19 passed, 3 failed"),
          security: check("passed", "no findings"),
          static_analysis: check("passed", "clean"),
          error: null,
          duration_ms: 1500,
          created_at: "2026-10-09T10:01:12Z",
        },
      ],
    },
    {
      id: "p2",
      iteration: 2,
      status: "passed",
      diff: DIFF,
      files_changed: 1,
      additions: 1,
      deletions: 1,
      created_at: "2026-10-09T10:01:30Z",
      validations: [
        {
          id: "v2",
          status: "passed",
          tests_status: "passed",
          security_status: "passed",
          static_status: "passed",
          tests: check("passed", "22 passed, 0 failed"),
          security: check("passed", "no findings"),
          static_analysis: check("passed", "clean"),
          error: null,
          duration_ms: 1400,
          created_at: "2026-10-09T10:01:32Z",
        },
      ],
    },
  ],
  pull_request: null,
  progress: null,
};
