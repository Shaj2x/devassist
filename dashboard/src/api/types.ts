/** Mirrors services/api/src/devassist_api/schemas.py. */

export type JobStatus =
  | "queued"
  | "planning"
  | "coding"
  | "testing"
  | "validating"
  | "debugging"
  | "reviewing"
  | "awaiting_review"
  | "changes_requested"
  | "approved"
  | "rejected"
  | "pr_opened"
  | "failed"
  | "cancelled";

export type CheckStatus = "passed" | "failed" | "skipped" | "error";

export interface User {
  id: string;
  login: string;
  avatar_url: string | null;
}

export interface Me {
  user: User;
  auth_mode: "dev" | "token";
  github_connected: boolean;
}

export interface Snapshot {
  id: string;
  commit_sha: string;
  status: "pending" | "indexing" | "ready" | "failed";
  file_count: number;
  chunk_count: number;
  embedding_model: string | null;
  error: string | null;
  created_at: string;
  finished_at: string | null;
}

export interface Repo {
  id: string;
  full_name: string;
  clone_url: string;
  default_branch: string;
  config: Record<string, unknown>;
  is_github: boolean;
  created_at: string;
  latest_snapshot: Snapshot | null;
}

export interface JobSummary {
  id: string;
  repo_id: string;
  repo_name: string;
  task: string;
  status: JobStatus;
  current_iteration: number;
  max_iterations: number;
  total_cost_usd: string;
  created_at: string;
  completed_at: string | null;
  pr_url: string | null;
}

export type AgentName = "planner" | "coder" | "tester" | "debugger" | "reviewer";

export interface StepSummary {
  id: string;
  sequence: number;
  iteration: number;
  agent: AgentName;
  status: "running" | "succeeded" | "failed";
  model: string | null;
  input_tokens: number;
  output_tokens: number;
  cost_usd: string;
  latency_ms: number | null;
  error: string | null;
  started_at: string;
  finished_at: string | null;
}

export interface StepDetail extends StepSummary {
  prompt: { system?: string; messages?: { role: string; content: string }[]; [key: string]: unknown };
  response: string | null;
  parsed_output: Record<string, unknown> | null;
}

export interface Finding {
  message: string;
  tool?: string | null;
  rule_id?: string | null;
  severity?: string | null;
  file?: string | null;
  line?: number | null;
}

export interface CheckResult {
  status: CheckStatus;
  tool?: string | null;
  summary?: string | null;
  passed?: number;
  failed?: number;
  skipped?: number;
  findings?: Finding[];
  log?: string | null;
  duration_ms?: number;
}

export interface Validation {
  id: string;
  status: "queued" | "running" | "passed" | "failed" | "error" | "timeout";
  tests_status: CheckStatus | null;
  security_status: CheckStatus | null;
  static_status: CheckStatus | null;
  tests: CheckResult | null;
  security: CheckResult | null;
  static_analysis: CheckResult | null;
  error: string | null;
  duration_ms: number | null;
  created_at: string;
}

export interface Patch {
  id: string;
  iteration: number;
  status: "generated" | "validating" | "passed" | "failed" | "superseded" | "approved" | "rejected";
  diff: string;
  files_changed: number;
  additions: number;
  deletions: number;
  created_at: string;
  validations: Validation[];
}

export interface Plan {
  summary?: string;
  files_to_change?: { path: string; change: string }[];
  steps?: string[];
  risks?: string[];
  test_strategy?: string;
}

export interface Review {
  summary?: string;
  risk_level?: "low" | "medium" | "high";
  concerns?: string[];
  suggested_followups?: string[];
  ready_to_merge?: boolean;
}

export interface Progress {
  status?: JobStatus;
  current_agent?: AgentName | null;
  current_step?: string | null;
  iteration?: number;
  validation?: string;
  error?: string | null;
  updated_at?: string;
  [key: string]: unknown;
}

export interface JobDetail extends JobSummary {
  repo_is_github: boolean;
  base_commit_sha: string | null;
  plan: Plan | null;
  review: Review | null;
  review_feedback: string | null;
  error: string | null;
  total_input_tokens: number;
  total_output_tokens: number;
  steps: StepSummary[];
  patches: Patch[];
  pull_request: { number: number; url: string; branch_name: string; state: string } | null;
  progress: Progress | null;
}

/** Statuses after which nothing happens without a human. */
export const SETTLED: ReadonlySet<JobStatus> = new Set([
  "awaiting_review",
  "approved",
  "rejected",
  "pr_opened",
  "failed",
  "cancelled",
]);
