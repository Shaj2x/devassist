import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { JOB, ME, mockApi, renderAt } from "../test/api";
import { JobPage } from "./JobPage";

function renderJob() {
  return renderAt("/jobs/j1", "/jobs/:jobId", <JobPage />);
}

describe("JobPage", () => {
  it("shows the plan, the agent timeline, the review and the final patch", async () => {
    mockApi({ "GET /v1/jobs/j1": JOB, "GET /v1/auth/me": ME });
    renderJob();

    expect(await screen.findByRole("heading", { name: JOB.task })).toBeInTheDocument();
    expect(screen.getByText("Awaiting review")).toBeInTheDocument();
    expect(screen.getByText("Correct the century rule")).toBeInTheDocument();
    expect(screen.getByText("Fixes leap years for century years.")).toBeInTheDocument();
    expect(screen.getByText("low risk")).toBeInTheDocument();
    expect(screen.getByText("Iteration 1")).toBeInTheDocument();
    expect(screen.getByText("Debugger")).toBeInTheDocument();

    // The newest patch is shown: its validation passed.
    expect(screen.getByRole("tab", { name: "#2", selected: true })).toBeInTheDocument();
    expect(screen.getByText("22 passed, 0 failed")).toBeInTheDocument();
    expect(screen.getAllByText(/year % 400 == 0/)).not.toHaveLength(0);
  });

  it("switches to an earlier patch and its failed validation", async () => {
    mockApi({ "GET /v1/jobs/j1": JOB, "GET /v1/auth/me": ME });
    renderJob();

    await userEvent.click(await screen.findByRole("tab", { name: "#1" }));
    expect(screen.getByText("19 passed, 3 failed")).toBeInTheDocument();
    expect(screen.getByText("superseded")).toBeInTheDocument();
  });

  it("opens an agent step with its prompt", async () => {
    mockApi({
      "GET /v1/jobs/j1": JOB,
      "GET /v1/auth/me": ME,
      "GET /v1/jobs/j1/steps/st1": {
        ...JOB.steps[0],
        prompt: { system: "You are the Planner.", messages: [{ role: "user", content: "Task: fix it" }] },
        response: '{"summary":"s"}',
        parsed_output: { summary: "Correct the century rule" },
      },
    });
    renderJob();

    await userEvent.click(await screen.findByRole("button", { name: /Planner/ }));
    const panel = await screen.findByRole("region", { name: "Agent step" });
    expect(await within(panel).findByText(/"summary": "Correct the century rule"/)).toBeInTheDocument();
    await userEvent.click(within(panel).getByRole("tab", { name: "prompt" }));
    expect(within(panel).getByText("You are the Planner.")).toBeInTheDocument();
  });

  it("approves a local job", async () => {
    const calls = mockApi({
      "GET /v1/jobs/j1": JOB,
      "GET /v1/auth/me": ME,
      "POST /v1/jobs/j1/approve": { ...JOB, status: "approved" },
    });
    renderJob();

    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    await waitFor(() => {
      expect(screen.getByText("Approved")).toBeInTheDocument();
    });
    expect(calls.some((c) => c.method === "POST" && c.path === "/v1/jobs/j1/approve")).toBe(true);
    expect(screen.queryByRole("region", { name: "Review" })).not.toBeInTheDocument();
  });

  it("sends feedback when requesting changes", async () => {
    const calls = mockApi({
      "GET /v1/jobs/j1": JOB,
      "GET /v1/auth/me": ME,
      "POST /v1/jobs/j1/request-changes": { ...JOB, status: "changes_requested", review_feedback: "Handle 1582" },
    });
    renderJob();

    await userEvent.click(await screen.findByRole("button", { name: "Request changes" }));
    await userEvent.type(screen.getByLabelText("What should change?"), "Handle 1582");
    await userEvent.click(screen.getByRole("button", { name: "Send back to the agents" }));

    expect(await screen.findByText("Changes requested")).toBeInTheDocument();
    const call = calls.find((c) => c.path === "/v1/jobs/j1/request-changes");
    expect(call?.body).toEqual({ feedback: "Handle 1582" });
  });

  it("blocks opening a PR without a GitHub token and shows API errors", async () => {
    mockApi({ "GET /v1/jobs/j1": { ...JOB, repo_is_github: true }, "GET /v1/auth/me": ME });
    renderJob();

    expect(await screen.findByRole("button", { name: "Approve & open PR" })).toBeDisabled();
    expect(screen.getByText(/needs a GitHub token/)).toBeInTheDocument();
  });

  it("reports a failed job and offers a retry", async () => {
    mockApi({
      "GET /v1/jobs/j1": { ...JOB, status: "failed", error: "validation still failed after 3 iteration(s)" },
      "GET /v1/auth/me": ME,
    });
    renderJob();

    expect(await screen.findByText("validation still failed after 3 iteration(s)")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry with feedback" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });
});

class FakeEventSource {
  static last: FakeEventSource | null = null;
  closed = false;
  private listeners = new Map<string, ((e: MessageEvent<string>) => void)[]>();
  constructor(readonly url: string) {
    FakeEventSource.last = this;
  }
  addEventListener(type: string, fn: (e: MessageEvent<string>) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]);
  }
  emit(type: string, data: unknown) {
    for (const fn of this.listeners.get(type) ?? []) fn(new MessageEvent(type, { data: JSON.stringify(data) }));
  }
  close() {
    this.closed = true;
  }
}

describe("JobPage while the agents run", () => {
  it("follows live progress over server-sent events and refetches", async () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    let fetches = 0;
    const running = { ...JOB, status: "coding" as const, review: null, patches: [], steps: JOB.steps.slice(0, 1) };
    mockApi({
      "GET /v1/jobs/j1": () => {
        fetches++;
        return { body: running };
      },
      "GET /v1/auth/me": ME,
    });
    renderJob();

    expect(await screen.findByText("Coding")).toBeInTheDocument();
    expect(screen.getByText("The agents have not produced a patch yet.")).toBeInTheDocument();
    const source = FakeEventSource.last;
    expect(source?.url).toBe("/api/v1/jobs/j1/events");

    act(() => {
      source?.emit("progress", { status: "coding", current_agent: "coder", current_step: "st2", iteration: 1 });
    });
    expect(await screen.findByText(/Coder is working · iteration 1/)).toBeInTheDocument();
    await waitFor(() => {
      expect(fetches).toBeGreaterThan(1);
    });

    // Between agent calls the stale agent is not shown; the phase is.
    act(() => {
      source?.emit("progress", { status: "validating", current_agent: "tester", current_step: null });
    });
    expect(await screen.findByText(/in the sandbox/)).toBeInTheDocument();
    expect(screen.queryByText(/is working/)).not.toBeInTheDocument();

    act(() => {
      source?.emit("end", {});
    });
    expect(source?.closed).toBe(true);
    vi.unstubAllGlobals();
  });
});
