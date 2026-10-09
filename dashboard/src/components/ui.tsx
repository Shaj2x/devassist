import type { ButtonHTMLAttributes, ReactNode } from "react";
import type { CheckStatus, JobStatus } from "../api/types";
import { cx, isRunning } from "../lib/ui";

type Tone = "neutral" | "blue" | "green" | "amber" | "red" | "violet";

const TONES: Record<Tone, string> = {
  neutral: "bg-slate-100 text-slate-700",
  blue: "bg-sky-50 text-sky-700",
  green: "bg-emerald-50 text-emerald-700",
  amber: "bg-amber-50 text-amber-800",
  red: "bg-red-50 text-red-700",
  violet: "bg-violet-50 text-violet-700",
};

const DOTS: Record<Tone, string> = {
  neutral: "bg-slate-400",
  blue: "bg-sky-500",
  green: "bg-emerald-500",
  amber: "bg-amber-500",
  red: "bg-red-500",
  violet: "bg-violet-500",
};

export function Badge({ tone, children, pulse = false }: { tone: Tone; children: ReactNode; pulse?: boolean }) {
  return (
    <span
      className={cx(
        "inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-medium",
        TONES[tone],
      )}
    >
      <span className={cx("h-1.5 w-1.5 rounded-full", DOTS[tone], pulse && "animate-pulse")} />
      {children}
    </span>
  );
}

const JOB_TONE: Record<JobStatus, Tone> = {
  queued: "neutral",
  planning: "blue",
  coding: "blue",
  testing: "blue",
  validating: "blue",
  debugging: "amber",
  reviewing: "blue",
  awaiting_review: "violet",
  changes_requested: "amber",
  approved: "green",
  pr_opened: "green",
  rejected: "neutral",
  failed: "red",
  cancelled: "neutral",
};

const JOB_LABEL: Record<JobStatus, string> = {
  queued: "Queued",
  planning: "Planning",
  coding: "Coding",
  testing: "Writing tests",
  validating: "Validating",
  debugging: "Debugging",
  reviewing: "Reviewing",
  awaiting_review: "Awaiting review",
  changes_requested: "Changes requested",
  approved: "Approved",
  pr_opened: "PR opened",
  rejected: "Rejected",
  failed: "Failed",
  cancelled: "Cancelled",
};

export function JobStatusBadge({ status }: { status: JobStatus }) {
  return (
    <Badge tone={JOB_TONE[status]} pulse={isRunning(status)}>
      {JOB_LABEL[status]}
    </Badge>
  );
}

const CHECK_TONE: Record<CheckStatus, Tone> = {
  passed: "green",
  failed: "red",
  skipped: "neutral",
  error: "amber",
};

export function CheckBadge({ status }: { status: CheckStatus | null | undefined }) {
  if (!status) return <Badge tone="neutral">n/a</Badge>;
  return <Badge tone={CHECK_TONE[status]}>{status}</Badge>;
}

export function Card({ title, actions, children, className }: {
  title?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cx("rounded-lg border border-slate-200 bg-white", className)}>
      {(title ?? actions) !== undefined && (
        <header className="flex items-center justify-between gap-4 border-b border-slate-100 px-5 py-3">
          <h2 className="text-sm font-semibold text-slate-900">{title}</h2>
          {actions}
        </header>
      )}
      <div className="p-5">{children}</div>
    </section>
  );
}

type Variant = "primary" | "secondary" | "danger" | "ghost";

const VARIANTS: Record<Variant, string> = {
  primary: "bg-slate-900 text-white hover:bg-slate-700 disabled:bg-slate-400",
  secondary: "border border-slate-300 bg-white text-slate-800 hover:bg-slate-50 disabled:text-slate-400",
  danger: "border border-red-200 bg-white text-red-700 hover:bg-red-50 disabled:text-red-300",
  ghost: "text-slate-600 hover:bg-slate-100 hover:text-slate-900",
};

export function Button({
  variant = "secondary",
  busy = false,
  className,
  children,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; busy?: boolean }) {
  return (
    <button
      type="button"
      {...props}
      disabled={props.disabled === true || busy}
      className={cx(
        "inline-flex items-center justify-center gap-2 rounded-md px-3 py-1.5 text-sm font-medium transition-colors",
        "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-500 disabled:cursor-not-allowed",
        VARIANTS[variant],
        className,
      )}
    >
      {busy && <Spinner />}
      {children}
    </button>
  );
}

export function Spinner({ className }: { className?: string }) {
  return (
    <span
      aria-hidden
      className={cx("inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-current border-r-transparent", className)}
    />
  );
}

export function ErrorNote({ error }: { error: unknown }) {
  if (!error) return null;
  const message = error instanceof Error ? error.message : typeof error === "string" ? error : "Something went wrong";
  return (
    <p role="alert" className="rounded-md bg-red-50 px-3 py-2 text-sm text-red-700">
      {message}
    </p>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-slate-300 bg-white p-10 text-center text-sm text-slate-500">
      {children}
    </div>
  );
}

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <p className="flex items-center gap-2 p-6 text-sm text-slate-500">
      <Spinner /> {label}
    </p>
  );
}
