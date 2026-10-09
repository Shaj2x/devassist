import { Link, useSearchParams } from "react-router-dom";
import { useJobs, useRepos } from "../api/queries";
import { Empty, ErrorNote, JobStatusBadge, Loading } from "../components/ui";
import { formatCost, timeAgo } from "../lib/format";

export function JobsPage() {
  const [params, setParams] = useSearchParams();
  const repoId = params.get("repo") ?? undefined;
  const { data: jobs, error, isPending } = useJobs(repoId);
  const { data: repos } = useRepos();

  return (
    <section className="space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Jobs</h1>
          <p className="mt-1 text-slate-600">Every task the agents have worked on, newest first.</p>
        </div>
        <label className="flex items-center gap-2 text-sm text-slate-700">
          Repository
          <select
            value={repoId ?? ""}
            onChange={(e) => {
              setParams(e.target.value ? { repo: e.target.value } : {});
            }}
            className="rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm"
          >
            <option value="">All</option>
            {repos?.map((r) => (
              <option key={r.id} value={r.id}>
                {r.full_name}
              </option>
            ))}
          </select>
        </label>
      </div>

      {isPending && <Loading />}
      <ErrorNote error={error} />
      {jobs?.length === 0 && (
        <Empty>
          No jobs yet. Pick a repository on the <Link to="/" className="text-sky-700 hover:underline">Repositories</Link>{" "}
          page and start a task.
        </Empty>
      )}
      {jobs && jobs.length > 0 && (
        <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white">
          <table className="w-full text-sm">
            <thead className="border-b border-slate-100 text-left text-xs uppercase tracking-wide text-slate-500">
              <tr>
                <th className="px-5 py-2 font-medium">Task</th>
                <th className="px-3 py-2 font-medium">Status</th>
                <th className="px-3 py-2 font-medium">Iteration</th>
                <th className="px-3 py-2 text-right font-medium">Cost</th>
                <th className="px-5 py-2 text-right font-medium">Created</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {jobs.map((job) => (
                <tr key={job.id} className="hover:bg-slate-50">
                  <td className="max-w-md px-5 py-3">
                    <Link to={`/jobs/${job.id}`} className="block truncate font-medium text-slate-900 hover:underline">
                      {job.task.split("\n")[0]}
                    </Link>
                    <span className="text-xs text-slate-500">{job.repo_name}</span>
                  </td>
                  <td className="px-3 py-3">
                    <JobStatusBadge status={job.status} />
                  </td>
                  <td className="px-3 py-3 text-slate-600">
                    {job.current_iteration} / {job.max_iterations}
                  </td>
                  <td className="px-3 py-3 text-right tabular-nums text-slate-600">{formatCost(job.total_cost_usd)}</td>
                  <td className="whitespace-nowrap px-5 py-3 text-right text-slate-500">{timeAgo(job.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
