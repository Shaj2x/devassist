import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { apiDelete, apiGet, apiPost, streamUrl } from "./client";
import type { JobDetail, JobSummary, Me, Progress, Repo, StepDetail } from "./types";
import { SETTLED } from "./types";

export const keys = {
  me: ["me"] as const,
  repos: ["repos"] as const,
  repo: (id: string) => ["repos", id] as const,
  jobs: (repoId?: string) => (repoId ? (["jobs", { repoId }] as const) : (["jobs"] as const)),
  job: (id: string) => ["job", id] as const,
  step: (jobId: string, stepId: string) => ["job", jobId, "step", stepId] as const,
};

export function useMe() {
  return useQuery({ queryKey: keys.me, queryFn: () => apiGet<Me>("/v1/auth/me"), retry: false });
}

// --- repositories -------------------------------------------------------------

function indexing(repo: Repo): boolean {
  const status = repo.latest_snapshot?.status;
  return status === undefined || status === "pending" || status === "indexing";
}

export function useRepos() {
  return useQuery({
    queryKey: keys.repos,
    queryFn: () => apiGet<Repo[]>("/v1/repos"),
    // Notifications usually refresh this; poll as a fallback while indexing.
    refetchInterval: (query) => (query.state.data?.some(indexing) ? 3000 : false),
  });
}

export interface RegisterRepo {
  full_name: string;
  clone_url?: string;
  default_branch?: string;
}

export function useRegisterRepo() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body: RegisterRepo) => apiPost<Repo>("/v1/repos", body),
    onSuccess: () => client.invalidateQueries({ queryKey: keys.repos }),
  });
}

export function useReindexRepo() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiPost<{ status: string }>(`/v1/repos/${id}/reindex`),
    onSuccess: () => client.invalidateQueries({ queryKey: keys.repos }),
  });
}

export function useDeleteRepo() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => apiDelete(`/v1/repos/${id}`),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: keys.repos });
      await client.invalidateQueries({ queryKey: keys.jobs() });
    },
  });
}

// --- jobs ---------------------------------------------------------------------

export function useJobs(repoId?: string) {
  const query = repoId ? `?repo_id=${repoId}` : "";
  return useQuery({
    queryKey: keys.jobs(repoId),
    queryFn: () => apiGet<JobSummary[]>(`/v1/jobs${query}`),
    refetchInterval: (q) => (q.state.data?.some((j) => !SETTLED.has(j.status)) ? 5000 : false),
  });
}

export function useJob(id: string) {
  return useQuery({
    queryKey: keys.job(id),
    queryFn: () => apiGet<JobDetail>(`/v1/jobs/${id}`),
    // The event stream drives refreshes; this is a slow safety net.
    refetchInterval: (q) => (q.state.data && !SETTLED.has(q.state.data.status) ? 10_000 : false),
  });
}

export function useStep(jobId: string, stepId: string | null) {
  return useQuery({
    queryKey: keys.step(jobId, stepId ?? ""),
    queryFn: () => apiGet<StepDetail>(`/v1/jobs/${jobId}/steps/${stepId ?? ""}`),
    enabled: stepId !== null,
    staleTime: Infinity, // a finished step never changes
  });
}

export interface CreateJob {
  repo_id: string;
  task: string;
  max_iterations?: number;
}

export function useCreateJob() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateJob) => apiPost<JobSummary>("/v1/jobs", body),
    onSuccess: () => client.invalidateQueries({ queryKey: keys.jobs() }),
  });
}

type Decision =
  | { action: "approve" }
  | { action: "reject"; reason?: string }
  | { action: "request-changes"; feedback: string };

export function useJobDecision(jobId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (decision: Decision) => {
      const { action, ...body } = decision;
      return apiPost<JobDetail>(`/v1/jobs/${jobId}/${action}`, body);
    },
    onSuccess: async (job) => {
      client.setQueryData(keys.job(jobId), job);
      await client.invalidateQueries({ queryKey: keys.jobs() });
    },
  });
}

// --- live updates -------------------------------------------------------------

/**
 * Follows a running job over server-sent events. Each `progress` event is
 * returned for the live header and triggers a refetch of the job detail
 * (throttled by TanStack Query's de-duplication); `end` stops the stream.
 */
export function useJobProgress(jobId: string, active: boolean): Progress | null {
  const client = useQueryClient();
  const [progress, setProgress] = useState<Progress | null>(null);

  useEffect(() => {
    if (!active || typeof EventSource === "undefined") return;
    const source = new EventSource(streamUrl(`/v1/jobs/${jobId}/events`));
    source.addEventListener("progress", (event) => {
      try {
        setProgress(JSON.parse((event as MessageEvent<string>).data) as Progress);
      } catch {
        return;
      }
      void client.invalidateQueries({ queryKey: keys.job(jobId) });
    });
    source.addEventListener("end", () => {
      source.close();
      void client.invalidateQueries({ queryKey: keys.job(jobId) });
      void client.invalidateQueries({ queryKey: keys.jobs() });
    });
    return () => {
      source.close();
    };
  }, [jobId, active, client]);

  return active ? progress : null;
}

/** App-wide stream: refresh lists when a repo finishes indexing or a job finishes. */
export function useNotifications(enabled: boolean) {
  const client = useQueryClient();
  useEffect(() => {
    if (!enabled || typeof EventSource === "undefined") return;
    const source = new EventSource(streamUrl("/v1/events"));
    source.addEventListener("notification", (event) => {
      let note: { type?: string; job_id?: string };
      try {
        note = JSON.parse((event as MessageEvent<string>).data) as typeof note;
      } catch {
        return;
      }
      if (note.type === "repo.indexed") void client.invalidateQueries({ queryKey: keys.repos });
      if (note.type === "job.completed") {
        void client.invalidateQueries({ queryKey: keys.jobs() });
        if (note.job_id) void client.invalidateQueries({ queryKey: keys.job(note.job_id) });
      }
    });
    return () => {
      source.close();
    };
  }, [enabled, client]);
}
