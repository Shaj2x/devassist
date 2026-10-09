import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ME, REPO, mockApi, renderAt, reply } from "../test/api";
import { RepositoriesPage } from "./RepositoriesPage";

function renderPage() {
  return renderAt("/", "/", <RepositoriesPage />);
}

describe("RepositoriesPage", () => {
  it("lists repositories with their index status", async () => {
    mockApi({ "GET /v1/repos": [REPO], "GET /v1/auth/me": ME });
    renderPage();

    expect(await screen.findByText("demo/datekit")).toBeInTheDocument();
    expect(screen.getByText("Indexed")).toBeInTheDocument();
    expect(screen.getByText(/7 files/)).toBeInTheDocument();
    expect(screen.getByText("local")).toBeInTheDocument();
  });

  it("registers a local repository", async () => {
    const calls = mockApi({
      "GET /v1/repos": [],
      "GET /v1/auth/me": ME,
      "POST /v1/repos": reply(201, { ...REPO, latest_snapshot: null }),
    });
    renderPage();

    await userEvent.type(await screen.findByLabelText("GitHub repository"), "demo/datekit");
    await userEvent.type(screen.getByLabelText(/Clone URL/), "file:///sample-repos/datekit.git");
    await userEvent.click(screen.getByRole("button", { name: "Register & index" }));

    await waitFor(() => {
      expect(calls.find((c) => c.method === "POST")?.body).toEqual({
        full_name: "demo/datekit",
        clone_url: "file:///sample-repos/datekit.git",
      });
    });
  });

  it("hides local clone URLs outside dev mode and shows API errors", async () => {
    mockApi({
      "GET /v1/repos": [],
      "GET /v1/auth/me": { ...ME, auth_mode: "token" },
      "POST /v1/repos": reply(404, { detail: "octo/missing not found on GitHub" }),
    });
    renderPage();

    await userEvent.type(await screen.findByLabelText("GitHub repository"), "octo/missing");
    expect(screen.queryByLabelText(/Clone URL/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Register & index" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("octo/missing not found on GitHub");
  });

  it("starts a task and opens the job", async () => {
    const calls = mockApi({
      "GET /v1/repos": [REPO],
      "GET /v1/auth/me": ME,
      "POST /v1/jobs": reply(202, { id: "j9", status: "queued" }),
    });
    renderPage();

    await userEvent.click(await screen.findByRole("button", { name: "New task" }));
    await userEvent.click(screen.getByRole("button", { name: "Fix the failing test in datekit/calendar.py" }));
    await userEvent.click(screen.getByRole("button", { name: "Start agents" }));

    expect(await screen.findByText("elsewhere")).toBeInTheDocument(); // navigated to /jobs/j9
    expect(calls.find((c) => c.path === "/v1/jobs")?.body).toEqual({
      repo_id: "r1",
      task: "Fix the failing test in datekit/calendar.py",
      max_iterations: 3,
    });
  });

  it("cannot start a task until the repository is indexed", async () => {
    mockApi({
      "GET /v1/repos": [{ ...REPO, latest_snapshot: { ...REPO.latest_snapshot, status: "indexing" } }],
      "GET /v1/auth/me": ME,
    });
    renderPage();

    expect(await screen.findByText("Indexing")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New task" })).toBeDisabled();
  });
});
