import { useState } from "react";
import { useJobDecision } from "../api/queries";
import type { JobDetail } from "../api/types";
import { Button, ErrorNote } from "./ui";

/** Approve (opens the PR), reject, or send back to the agents with feedback. */
export function ReviewActions({ job, githubConnected }: { job: JobDetail; githubConnected: boolean }) {
  const decide = useJobDecision(job.id);
  const [mode, setMode] = useState<"idle" | "changes" | "reject">("idle");
  const [text, setText] = useState("");

  const reviewable = job.status === "awaiting_review";
  const retryable = job.status === "failed";
  if (!reviewable && !retryable) return null;

  const needsToken = job.repo_is_github && !githubConnected;
  const submit = () => {
    const reason = text.trim();
    if (mode === "changes") decide.mutate({ action: "request-changes", feedback: reason });
    if (mode === "reject") decide.mutate(reason ? { action: "reject", reason } : { action: "reject" });
  };

  return (
    <section aria-label="Review" className="rounded-lg border border-violet-200 bg-violet-50/50 p-5">
      <h2 className="text-sm font-semibold text-slate-900">
        {reviewable ? "Your review" : "This job failed"}
      </h2>
      <p className="mt-1 text-sm text-slate-600">
        {reviewable
          ? job.repo_is_github
            ? "Approving opens a pull request with the validated patch, based on the commit the agents worked from."
            : "This is a local repository, so approving records the decision without opening a pull request."
          : "Give the agents a hint and run them again, or reject the job."}
      </p>

      {mode === "idle" ? (
        <div className="mt-4 flex flex-wrap gap-2">
          {reviewable && (
            <Button
              variant="primary"
              busy={decide.isPending}
              onClick={() => {
                decide.mutate({ action: "approve" });
              }}
              disabled={needsToken}
            >
              {job.repo_is_github ? "Approve & open PR" : "Approve"}
            </Button>
          )}
          <Button
            onClick={() => {
              setMode("changes");
            }}
          >
            {reviewable ? "Request changes" : "Retry with feedback"}
          </Button>
          <Button
            variant="danger"
            onClick={() => {
              setMode("reject");
            }}
          >
            Reject
          </Button>
        </div>
      ) : (
        <form
          className="mt-4 space-y-2"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <label className="block text-sm font-medium text-slate-800" htmlFor="review-text">
            {mode === "changes" ? "What should change?" : "Reason (optional)"}
          </label>
          <textarea
            id="review-text"
            value={text}
            onChange={(e) => {
              setText(e.target.value);
            }}
            rows={3}
            required={mode === "changes"}
            minLength={mode === "changes" ? 3 : undefined}
            className="w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm focus:border-sky-500 focus:outline-none"
            placeholder={mode === "changes" ? "e.g. Also handle years before 1582" : ""}
          />
          <div className="flex gap-2">
            <Button type="submit" variant={mode === "reject" ? "danger" : "primary"} busy={decide.isPending}>
              {mode === "changes" ? "Send back to the agents" : "Reject job"}
            </Button>
            <Button
              variant="ghost"
              onClick={() => {
                setMode("idle");
                setText("");
              }}
            >
              Cancel
            </Button>
          </div>
        </form>
      )}
      {needsToken && reviewable && (
        <p className="mt-3 text-xs text-amber-800">
          Opening a pull request needs a GitHub token: set GITHUB_TOKEN (dev mode) or sign in with one.
        </p>
      )}
      <div className="mt-3">
        <ErrorNote error={decide.error} />
      </div>
    </section>
  );
}
