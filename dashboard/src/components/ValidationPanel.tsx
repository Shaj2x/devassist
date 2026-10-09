import { useState } from "react";
import type { CheckResult, Validation } from "../api/types";
import { formatDuration } from "../lib/format";
import { cx } from "../lib/ui";
import { CheckBadge } from "./ui";

const SECTIONS = [
  { key: "tests", label: "Tests" },
  { key: "security", label: "Security" },
  { key: "static_analysis", label: "Static analysis" },
] as const;

export function ValidationPanel({ validation }: { validation: Validation | undefined }) {
  if (!validation) return <p className="text-sm text-slate-500">Not validated yet.</p>;
  return (
    <div className="space-y-3">
      <p className="text-xs text-slate-500">
        Sandbox run {validation.status} in {formatDuration(validation.duration_ms)}
        {validation.error && <span className="text-red-700"> · {validation.error}</span>}
      </p>
      <div className="grid gap-3 md:grid-cols-3 [&>*]:min-w-0">
        {SECTIONS.map(({ key, label }) => (
          <CheckCard key={key} label={label} check={validation[key]} />
        ))}
      </div>
    </div>
  );
}

function CheckCard({ label, check }: { label: string; check: CheckResult | null }) {
  const [showLog, setShowLog] = useState(false);
  const findings = check?.findings ?? [];
  return (
    <div className="rounded-md border border-slate-200 p-3">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium text-slate-800">{label}</span>
        <CheckBadge status={check?.status} />
      </div>
      {check?.tool && <p className="mt-1 font-mono text-[11px] text-slate-400">{check.tool}</p>}
      {check?.summary && <p className="mt-2 text-sm text-slate-700">{check.summary}</p>}
      {findings.length > 0 && (
        <ul className="mt-2 space-y-1.5">
          {findings.slice(0, 8).map((f, i) => (
            <li key={i} className="break-words text-xs text-slate-700">
              <span
                className={cx("block break-all font-mono", f.severity === "high" ? "text-red-700" : "text-amber-700")}
              >
                {f.rule_id ?? f.severity ?? "finding"}
              </span>
              {f.file && (
                <span className="font-mono text-slate-500">
                  {f.file}
                  {f.line ? `:${String(f.line)}` : ""}{" "}
                </span>
              )}
              <span className="line-clamp-4" title={f.message}>
                {f.message}
              </span>
            </li>
          ))}
          {findings.length > 8 && <li className="text-xs text-slate-500">+{findings.length - 8} more</li>}
        </ul>
      )}
      {check?.log && (
        <>
          <button
            type="button"
            className="mt-2 text-xs font-medium text-sky-700 hover:underline"
            onClick={() => {
              setShowLog(!showLog);
            }}
          >
            {showLog ? "Hide output" : "Show output"}
          </button>
          {showLog && (
            <pre className="mt-2 max-h-72 overflow-auto rounded bg-slate-900 p-2 text-[11px] leading-4 text-slate-100">
              {check.log}
            </pre>
          )}
        </>
      )}
    </div>
  );
}
