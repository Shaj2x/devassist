import { useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { useJob, useJobProgress, useMe } from "../api/queries";
import type { JobDetail, Patch, Plan, Progress, Review } from "../api/types";
import { SETTLED } from "../api/types";
import { DiffView } from "../components/DiffView";
import { ReviewActions } from "../components/ReviewActions";
import { StepDetail } from "../components/StepDetail";
import { Timeline } from "../components/Timeline";
import { Badge, Card, ErrorNote, JobStatusBadge, Loading, Spinner } from "../components/ui";
import { ValidationPanel } from "../components/ValidationPanel";
import { formatCost, formatTokens, shortSha, timeAgo } from "../lib/format";
import { agentLabel, cx } from "../lib/ui";

export function JobPage() {
  const { jobId = "" } = useParams();
  const { data: job, error, isPending } = useJob(jobId);
  const { data: me } = useMe();
  const live = job !== undefined && !SETTLED.has(job.status);
  const progress = useJobProgress(jobId, live);
  const [step, setStep] = useState<string | null>(null);

  if (isPending) return <Loading label="Loading job…" />;
  if (!job) return <ErrorNote error={error} />;

  return (
    <article className="space-y-6">
      <Header job={job} progress={live ? (progress ?? job.progress) : null} />
      {job.error && (
        <p role="alert" className="rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800">
          {job.error}
        </p>
      )}
      {job.status === "changes_requested" && job.review_feedback && (
        <p className="rounded-md border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
          <span className="font-medium">Changes requested:</span> {job.review_feedback}
        </p>
      )}
      <ReviewActions job={job} githubConnected={me?.github_connected ?? false} />

      <div className="grid gap-6 lg:grid-cols-3">
        <div className="space-y-6 lg:col-span-2">
          {step && (
            <StepDetail
              jobId={job.id}
              stepId={step}
              onClose={() => {
                setStep(null);
              }}
            />
          )}
          {job.review && <ReviewCard review={job.review} />}
          <PatchesCard patches={job.patches} running={live} />
        </div>
        <div className="space-y-6">
          <Card title="Agent timeline">
            <Timeline steps={job.steps} selected={step} onSelect={setStep} />
          </Card>
          {job.plan && <PlanCard plan={job.plan} />}
        </div>
      </div>
    </article>
  );
}

function Header({ job, progress }: { job: JobDetail; progress: Progress | null }) {
  const [title, ...rest] = job.task.split("\n");
  const detail = rest.join("\n").trim();
  // current_agent lingers after a step ends; current_step is set only mid-call.
  const agent = progress?.current_step ? progress.current_agent : null;
  const between = progress && !agent ? BETWEEN_STEPS[progress.status ?? job.status] : undefined;
  return (
    <header className="space-y-3">
      <Link to="/jobs" className="text-sm text-slate-500 hover:text-slate-800">
        ← Jobs
      </Link>
      <div className="flex flex-wrap items-start gap-3">
        <h1 className="min-w-0 flex-1 text-2xl font-semibold tracking-tight">{title}</h1>
        <JobStatusBadge status={job.status} />
      </div>
      {detail && <p className="whitespace-pre-wrap text-sm text-slate-600">{detail}</p>}
      {agent && (
        <p className="flex items-center gap-2 text-sm text-sky-800" aria-live="polite">
          <Spinner /> {agentLabel(agent)} is working
          {progress?.iteration ? ` · iteration ${String(progress.iteration)}` : ""}
        </p>
      )}
      {between && (
        <p className="flex items-center gap-2 text-sm text-sky-800" aria-live="polite">
          <Spinner /> {between}
        </p>
      )}
      <dl className="flex flex-wrap gap-x-6 gap-y-1 text-sm">
        <Stat label="Repository" value={job.repo_name} />
        <Stat label="Base" value={<span className="font-mono">{shortSha(job.base_commit_sha)}</span>} />
        <Stat label="Iterations" value={`${String(job.current_iteration)} / ${String(job.max_iterations)}`} />
        <Stat
          label="Tokens"
          value={`${formatTokens(job.total_input_tokens)} in · ${formatTokens(job.total_output_tokens)} out`}
        />
        <Stat label="Cost" value={formatCost(job.total_cost_usd)} />
        <Stat label="Created" value={timeAgo(job.created_at)} />
        {job.pull_request && (
          <Stat
            label="Pull request"
            value={
              <a href={job.pull_request.url} target="_blank" rel="noreferrer" className="text-sky-700 hover:underline">
                #{job.pull_request.number}
              </a>
            }
          />
        )}
      </dl>
    </header>
  );
}

const BETWEEN_STEPS: Partial<Record<JobDetail["status"], string>> = {
  queued: "Waiting for an orchestrator worker",
  changes_requested: "Waiting for an orchestrator worker",
  validating: "Running tests, security scan and static analysis in the sandbox",
};

function Stat({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="flex gap-1.5">
      <dt className="text-slate-500">{label}</dt>
      <dd className="font-medium text-slate-800">{value}</dd>
    </div>
  );
}

const RISK_TONE = { low: "green", medium: "amber", high: "red" } as const;

function ReviewCard({ review }: { review: Review }) {
  return (
    <Card
      title="Reviewer agent"
      actions={review.risk_level && <Badge tone={RISK_TONE[review.risk_level]}>{review.risk_level} risk</Badge>}
    >
      {review.summary && <p className="text-sm text-slate-800">{review.summary}</p>}
      <List title="Check before merging" items={review.concerns} />
      <List title="Suggested follow-ups" items={review.suggested_followups} />
    </Card>
  );
}

function PlanCard({ plan }: { plan: Plan }) {
  return (
    <Card title="Plan">
      {plan.summary && <p className="text-sm text-slate-800">{plan.summary}</p>}
      {plan.files_to_change && plan.files_to_change.length > 0 && (
        <ul className="mt-3 space-y-1">
          {plan.files_to_change.map((f) => (
            <li key={f.path} className="text-xs">
              <span className="font-mono text-slate-800">{f.path}</span>
              <span className="text-slate-500"> · {f.change}</span>
            </li>
          ))}
        </ul>
      )}
      <List title="Steps" items={plan.steps} ordered />
      <List title="Risks" items={plan.risks} />
      {plan.test_strategy && (
        <p className="mt-3 text-xs text-slate-600">
          <span className="font-medium text-slate-700">Testing: </span>
          {plan.test_strategy}
        </p>
      )}
    </Card>
  );
}

function List({ title, items, ordered = false }: { title: string; items: string[] | undefined; ordered?: boolean }) {
  if (!items || items.length === 0) return null;
  const Tag = ordered ? "ol" : "ul";
  return (
    <div className="mt-3">
      <p className="text-xs font-semibold uppercase tracking-wide text-slate-400">{title}</p>
      <Tag className={cx("mt-1 space-y-0.5 pl-4 text-sm text-slate-700", ordered ? "list-decimal" : "list-disc")}>
        {items.map((item, i) => (
          <li key={i}>{item}</li>
        ))}
      </Tag>
    </div>
  );
}

const PATCH_TONE = {
  generated: "neutral",
  validating: "blue",
  passed: "green",
  failed: "red",
  superseded: "neutral",
  approved: "green",
  rejected: "neutral",
} as const;

function PatchesCard({ patches, running }: { patches: Patch[]; running: boolean }) {
  const [chosen, setChosen] = useState<string | null>(null);
  const latest = patches.at(-1);
  // Follow the newest patch unless the reviewer picked an older one.
  const patch = patches.find((p) => p.id === chosen) ?? latest;

  if (!patch) {
    return (
      <Card title="Patches">
        <p className="flex items-center gap-2 text-sm text-slate-500">
          {running && <Spinner />}
          {running ? "The agents have not produced a patch yet." : "No patch was produced."}
        </p>
      </Card>
    );
  }
  const validation = patch.validations.at(-1);
  return (
    <Card
      title="Patches"
      actions={
        <div className="flex gap-1" role="tablist" aria-label="Patch iterations">
          {patches.map((p) => (
            <button
              key={p.id}
              type="button"
              role="tab"
              aria-selected={p.id === patch.id}
              onClick={() => {
                setChosen(p.id);
              }}
              className={cx(
                "rounded-md px-2.5 py-1 text-xs font-medium",
                p.id === patch.id ? "bg-slate-900 text-white" : "text-slate-600 hover:bg-slate-100",
              )}
            >
              #{p.iteration}
            </button>
          ))}
        </div>
      }
    >
      <div className="space-y-5">
        <div className="flex flex-wrap items-center gap-3 text-sm">
          <Badge tone={PATCH_TONE[patch.status]} pulse={patch.status === "validating"}>
            {patch.status}
          </Badge>
          <span className="text-slate-600">
            {patch.files_changed} files · <span className="text-emerald-700">+{patch.additions}</span>{" "}
            <span className="text-red-700">−{patch.deletions}</span>
          </span>
        </div>
        <ValidationPanel validation={validation} />
        <DiffView diff={patch.diff} />
      </div>
    </Card>
  );
}
