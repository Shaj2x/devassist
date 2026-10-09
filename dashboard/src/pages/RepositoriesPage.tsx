import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useCreateJob, useDeleteRepo, useMe, useRegisterRepo, useReindexRepo, useRepos } from "../api/queries";
import type { Repo } from "../api/types";
import { Badge, Button, Card, Empty, ErrorNote, Loading, Spinner } from "../components/ui";
import { shortSha, timeAgo } from "../lib/format";

export function RepositoriesPage() {
  const { data: repos, error, isPending } = useRepos();
  const [selected, setSelected] = useState<string | null>(null);
  const active = repos?.find((r) => r.id === selected) ?? null;

  return (
    <section className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Repositories</h1>
        <p className="mt-1 text-slate-600">
          Connect a repository, then describe a change in plain English. DevAssist plans it, writes the code and
          tests, and validates it in a sandbox before you review it.
        </p>
      </div>

      <RegisterForm />

      {isPending && <Loading />}
      <ErrorNote error={error} />
      {repos?.length === 0 && <Empty>No repositories yet. Register one above.</Empty>}
      {repos && repos.length > 0 && (
        <ul className="divide-y divide-slate-100 overflow-hidden rounded-lg border border-slate-200 bg-white">
          {repos.map((repo) => (
            <RepoRow
              key={repo.id}
              repo={repo}
              selected={repo.id === selected}
              onSelect={() => {
                setSelected(repo.id === selected ? null : repo.id);
              }}
            />
          ))}
        </ul>
      )}
      {active && <NewTaskForm key={active.id} repo={active} />}
    </section>
  );
}

function RegisterForm() {
  const { data: me } = useMe();
  const register = useRegisterRepo();
  const [name, setName] = useState("");
  const [cloneUrl, setCloneUrl] = useState("");
  const localAllowed = me?.auth_mode === "dev";

  return (
    <Card title="Register a repository">
      <form
        className="flex flex-wrap items-end gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          const body = cloneUrl.trim()
            ? { full_name: name.trim(), clone_url: cloneUrl.trim() }
            : { full_name: name.trim() };
          register.mutate(body, {
            onSuccess: () => {
              setName("");
              setCloneUrl("");
            },
          });
        }}
      >
        <label className="flex min-w-56 flex-1 flex-col gap-1 text-sm">
          <span className="font-medium text-slate-700">GitHub repository</span>
          <input
            required
            value={name}
            onChange={(e) => {
              setName(e.target.value);
            }}
            pattern="[\w.\-]+/[\w.\-]+"
            placeholder="owner/name"
            className="rounded-md border border-slate-300 px-3 py-1.5 font-mono text-sm focus:border-sky-500 focus:outline-none"
          />
        </label>
        {localAllowed && (
          <label className="flex min-w-56 flex-1 flex-col gap-1 text-sm">
            <span className="font-medium text-slate-700">
              Clone URL <span className="font-normal text-slate-400">(optional, local repos)</span>
            </span>
            <input
              value={cloneUrl}
              onChange={(e) => {
                setCloneUrl(e.target.value);
              }}
              placeholder="file:///sample-repos/datekit.git"
              className="rounded-md border border-slate-300 px-3 py-1.5 font-mono text-sm focus:border-sky-500 focus:outline-none"
            />
          </label>
        )}
        <Button type="submit" variant="primary" busy={register.isPending}>
          Register &amp; index
        </Button>
      </form>
      {localAllowed && (
        <p className="mt-2 text-xs text-slate-500">
          Try the bundled sample: <code className="font-mono">demo/datekit</code> with clone URL{" "}
          <code className="font-mono">file:///sample-repos/datekit.git</code>.
        </p>
      )}
      <div className="mt-3">
        <ErrorNote error={register.error} />
      </div>
    </Card>
  );
}

function IndexStatus({ repo }: { repo: Repo }) {
  const snap = repo.latest_snapshot;
  if (!snap || snap.status === "pending" || snap.status === "indexing") {
    return (
      <span className="flex items-center gap-1.5 text-xs text-sky-700">
        <Spinner /> Indexing
      </span>
    );
  }
  if (snap.status === "failed") {
    return (
      <span title={snap.error ?? undefined}>
        <Badge tone="red">Index failed</Badge>
      </span>
    );
  }
  return (
    <span className="text-xs text-slate-500">
      <Badge tone="green">Indexed</Badge>{" "}
      <span className="ml-1 font-mono">{shortSha(snap.commit_sha)}</span> · {snap.file_count} files ·{" "}
      {snap.chunk_count} chunks · {timeAgo(snap.finished_at ?? snap.created_at)}
    </span>
  );
}

function RepoRow({ repo, selected, onSelect }: { repo: Repo; selected: boolean; onSelect: () => void }) {
  const reindex = useReindexRepo();
  const remove = useDeleteRepo();
  const ready = repo.latest_snapshot?.status === "ready";
  return (
    <li className={selected ? "bg-slate-50" : undefined}>
      <div className="flex flex-col gap-3 px-5 py-3 sm:flex-row sm:items-center sm:gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <Link to={`/jobs?repo=${repo.id}`} className="truncate font-medium text-slate-900 hover:underline">
              {repo.full_name}
            </Link>
            {!repo.is_github && <Badge tone="neutral">local</Badge>}
            <span className="font-mono text-xs text-slate-400">{repo.default_branch}</span>
          </div>
          <div className="mt-1">
            <IndexStatus repo={repo} />
          </div>
        </div>
        <div className="flex shrink-0 gap-2">
          <Button variant={selected ? "secondary" : "primary"} disabled={!ready} onClick={onSelect}>
            {selected ? "Close" : "New task"}
          </Button>
          <Button
            variant="ghost"
            busy={reindex.isPending}
            onClick={() => {
              reindex.mutate(repo.id);
            }}
          >
            Reindex
          </Button>
          <Button
            variant="ghost"
            busy={remove.isPending}
            onClick={() => {
              if (window.confirm(`Remove ${repo.full_name}, its index and its jobs?`)) remove.mutate(repo.id);
            }}
          >
            Remove
          </Button>
        </div>
      </div>
    </li>
  );
}

const EXAMPLES = [
  "Fix the failing test in datekit/calendar.py",
  "Add input validation to parse_duration and raise ValueError on bad units",
];

function NewTaskForm({ repo }: { repo: Repo }) {
  const navigate = useNavigate();
  const create = useCreateJob();
  const [task, setTask] = useState("");
  const [iterations, setIterations] = useState(3);

  return (
    <Card title={`New task on ${repo.full_name}`}>
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate(
            { repo_id: repo.id, task: task.trim(), max_iterations: iterations },
            {
              onSuccess: (job) => {
                void navigate(`/jobs/${job.id}`);
              },
            },
          );
        }}
      >
        <label className="block text-sm">
          <span className="font-medium text-slate-700">What should change?</span>
          <textarea
            required
            minLength={3}
            rows={4}
            value={task}
            onChange={(e) => {
              setTask(e.target.value);
            }}
            placeholder="Describe the change as you would to a colleague…"
            className="mt-1 w-full rounded-md border border-slate-300 px-3 py-2 text-sm focus:border-sky-500 focus:outline-none"
          />
        </label>
        <div className="flex flex-wrap gap-2">
          {EXAMPLES.map((example) => (
            <button
              key={example}
              type="button"
              onClick={() => {
                setTask(example);
              }}
              className="rounded-full border border-slate-200 px-3 py-1 text-xs text-slate-600 hover:bg-slate-50"
            >
              {example}
            </button>
          ))}
        </div>
        <div className="flex items-center gap-4">
          <label className="flex items-center gap-2 text-sm text-slate-700">
            Max iterations
            <input
              type="number"
              min={1}
              max={10}
              value={iterations}
              onChange={(e) => {
                setIterations(Number(e.target.value));
              }}
              className="w-16 rounded-md border border-slate-300 px-2 py-1 text-sm"
            />
          </label>
          <Button type="submit" variant="primary" busy={create.isPending} className="ml-auto">
            Start agents
          </Button>
        </div>
        <ErrorNote error={create.error} />
      </form>
    </Card>
  );
}
