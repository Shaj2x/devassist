import { useState } from "react";
import { useStep } from "../api/queries";
import { formatCost, formatDuration } from "../lib/format";
import { agentLabel, cx } from "../lib/ui";
import { Button, ErrorNote, Loading } from "./ui";

type Tab = "output" | "prompt" | "raw";

/** One agent call: parsed output, the exact prompt, and the raw response. */
export function StepDetail({ jobId, stepId, onClose }: { jobId: string; stepId: string; onClose: () => void }) {
  const { data: step, error, isPending } = useStep(jobId, stepId);
  const [tab, setTab] = useState<Tab>("output");

  return (
    <section aria-label="Agent step" className="rounded-lg border border-slate-200 bg-white">
      <header className="flex items-center gap-3 border-b border-slate-100 px-5 py-3">
        <h2 className="text-sm font-semibold">
          {step ? `${agentLabel(step.agent)} · iteration ${String(step.iteration)}` : "Agent step"}
        </h2>
        {step && (
          <span className="text-xs text-slate-500">
            {step.model ?? "unknown model"} · {formatDuration(step.latency_ms)} · {formatCost(step.cost_usd)}
          </span>
        )}
        <Button variant="ghost" className="ml-auto" onClick={onClose} aria-label="Close step">
          ✕
        </Button>
      </header>
      <div className="flex gap-1 border-b border-slate-100 px-5 pt-2" role="tablist">
        {(["output", "prompt", "raw"] as const).map((t) => (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={tab === t}
            onClick={() => {
              setTab(t);
            }}
            className={cx(
              "-mb-px border-b-2 px-3 py-1.5 text-xs font-medium capitalize",
              tab === t ? "border-slate-900 text-slate-900" : "border-transparent text-slate-500 hover:text-slate-800",
            )}
          >
            {t === "raw" ? "Raw response" : t}
          </button>
        ))}
      </div>
      <div className="max-h-[32rem] overflow-auto p-5">
        {isPending && <Loading />}
        <ErrorNote error={error} />
        {step?.error && <ErrorNote error={step.error} />}
        {step && tab === "output" && <Json value={step.parsed_output} />}
        {step && tab === "prompt" && (
          <div className="space-y-3">
            {step.prompt.system && <Message role="system" content={step.prompt.system} />}
            {(step.prompt.messages ?? []).map((m, i) => (
              <Message key={i} role={m.role} content={m.content} />
            ))}
          </div>
        )}
        {step && tab === "raw" && <Pre text={step.response ?? "(no response)"} />}
      </div>
    </section>
  );
}

function Message({ role, content }: { role: string; content: string }) {
  return (
    <div>
      <p className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-slate-400">{role}</p>
      <Pre text={content} />
    </div>
  );
}

function Json({ value }: { value: unknown }) {
  if (value === null || value === undefined) return <p className="text-sm text-slate-500">No parsed output.</p>;
  return <Pre text={JSON.stringify(value, null, 2)} />;
}

function Pre({ text }: { text: string }) {
  return (
    <pre className="whitespace-pre-wrap break-words rounded-md bg-slate-50 p-3 font-mono text-xs leading-5 text-slate-800">
      {text}
    </pre>
  );
}
