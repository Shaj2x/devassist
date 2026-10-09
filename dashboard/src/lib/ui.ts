import type { AgentName, JobStatus } from "../api/types";

export function cx(...classes: (string | false | null | undefined)[]): string {
  return classes.filter(Boolean).join(" ");
}

const RUNNING: ReadonlySet<JobStatus> = new Set([
  "queued",
  "planning",
  "coding",
  "testing",
  "validating",
  "debugging",
  "reviewing",
  "changes_requested",
]);

export function isRunning(status: JobStatus): boolean {
  return RUNNING.has(status);
}

export const AGENTS: Record<AgentName, { label: string; color: string }> = {
  planner: { label: "Planner", color: "bg-sky-500" },
  coder: { label: "Coder", color: "bg-indigo-500" },
  tester: { label: "Tester", color: "bg-teal-500" },
  debugger: { label: "Debugger", color: "bg-amber-500" },
  reviewer: { label: "Reviewer", color: "bg-violet-500" },
};

export function agentLabel(agent: AgentName): string {
  return AGENTS[agent].label;
}
