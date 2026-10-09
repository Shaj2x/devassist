import type { StepSummary } from "../api/types";
import { formatCost, formatDuration, formatTokens } from "../lib/format";
import { AGENTS as AGENT_STYLE, cx } from "../lib/ui";
import { Badge, Spinner } from "./ui";

/** The agents' steps in order, grouped by loop iteration. */
export function Timeline({
  steps,
  selected,
  onSelect,
}: {
  steps: StepSummary[];
  selected: string | null;
  onSelect: (id: string) => void;
}) {
  if (steps.length === 0) {
    return <p className="text-sm text-slate-500">No agent has run yet.</p>;
  }
  const iterations = [...new Set(steps.map((s) => s.iteration))];
  return (
    <ol className="space-y-4">
      {iterations.map((iteration) => (
        <li key={iteration}>
          <p className="mb-1.5 text-[11px] font-semibold uppercase tracking-wide text-slate-400">
            Iteration {iteration}
          </p>
          <ol className="relative space-y-1 border-l border-slate-200 pl-4">
            {steps
              .filter((s) => s.iteration === iteration)
              .map((step) => (
                <li key={step.id}>
                  <span
                    className={cx(
                      "absolute -left-[5px] mt-3 h-2.5 w-2.5 rounded-full ring-2 ring-white",
                      AGENT_STYLE[step.agent].color,
                    )}
                  />
                  <button
                    type="button"
                    onClick={() => {
                      onSelect(step.id);
                    }}
                    aria-pressed={selected === step.id}
                    className={cx(
                      "flex w-full items-center gap-3 rounded-md px-2 py-1.5 text-left text-sm",
                      selected === step.id ? "bg-slate-100" : "hover:bg-slate-50",
                    )}
                  >
                    <span className="w-20 font-medium text-slate-900">{AGENT_STYLE[step.agent].label}</span>
                    {step.status === "running" ? (
                      <span className="flex items-center gap-1.5 text-xs text-sky-700">
                        <Spinner /> working
                      </span>
                    ) : step.status === "failed" ? (
                      <Badge tone="red">failed</Badge>
                    ) : (
                      <span className="text-xs text-slate-500">
                        {formatTokens(step.input_tokens)} in · {formatTokens(step.output_tokens)} out
                      </span>
                    )}
                    <span className="ml-auto whitespace-nowrap text-xs text-slate-400">
                      {formatDuration(step.latency_ms)} · {formatCost(step.cost_usd)}
                    </span>
                  </button>
                </li>
              ))}
          </ol>
        </li>
      ))}
    </ol>
  );
}
