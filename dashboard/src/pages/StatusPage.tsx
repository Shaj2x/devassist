import { useReadiness } from "../api/health";

export function StatusPage() {
  const { data, error, isPending } = useReadiness();

  return (
    <section>
      <h1 className="text-2xl font-semibold tracking-tight">System status</h1>
      <p className="mt-2 text-slate-600">Live readiness of the API and its dependencies.</p>

      <div className="mt-6 overflow-hidden rounded-lg border border-slate-200 bg-white">
        {isPending && <p className="p-6 text-slate-500">Checking…</p>}
        {error && (
          <p role="alert" className="p-6 text-red-700">
            API unreachable: {error.message}
          </p>
        )}
        {data && (
          <ul className="divide-y divide-slate-100">
            {Object.entries(data.checks).map(([name, result]) => (
              <li key={name} className="flex items-center justify-between px-6 py-4">
                <span className="font-medium capitalize">{name}</span>
                <StatusBadge ok={result === "ok"} detail={result} />
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}

function StatusBadge({ ok, detail }: { ok: boolean; detail: string }) {
  return (
    <span
      title={detail}
      className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ${
        ok ? "bg-emerald-50 text-emerald-700" : "bg-red-50 text-red-700"
      }`}
    >
      <span className={`h-1.5 w-1.5 rounded-full ${ok ? "bg-emerald-500" : "bg-red-500"}`} />
      {ok ? "Healthy" : detail}
    </span>
  );
}
