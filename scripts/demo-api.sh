#!/usr/bin/env bash
# Drive the whole pipeline through the REST API, the way the dashboard does:
# register the datekit sample, wait for indexing, submit a task, follow the
# job to review. Usage: scripts/demo-api.sh ["task"]   (stack must be up)
set -euo pipefail

API=${API_URL:-http://localhost:8000}
TASK=${1:-Fix the failing test in datekit/calendar.py}
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }

echo "==> registering demo/datekit"
repo=$(curl -fsS -X POST "$API/v1/repos" -H 'content-type: application/json' \
  -d '{"full_name": "demo/datekit", "clone_url": "file:///sample-repos/datekit.git"}' | json 'd["id"]')

echo "==> waiting for the indexer (repo.registered -> repo.indexed)"
for _ in $(seq 1 120); do
  snap=$(curl -fsS "$API/v1/repos/$repo" | json '(d["latest_snapshot"] or {}).get("status", "pending")')
  [ "$snap" = ready ] && break
  [ "$snap" = failed ] && { echo "indexing failed"; exit 1; }
  sleep 1
done

echo "==> submitting: $TASK"
job=$(curl -fsS -X POST "$API/v1/jobs" -H 'content-type: application/json' \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"repo_id": sys.argv[1], "task": sys.argv[2]}))' "$repo" "$TASK")" \
  | json 'd["id"]')
echo "    job $job"

last=""
for _ in $(seq 1 600); do
  status=$(curl -fsS "$API/v1/jobs/$job" | json 'd["status"]')
  [ "$status" != "$last" ] && echo "    $status" && last=$status
  case $status in awaiting_review|failed) break ;; esac
  sleep 1
done

curl -fsS "$API/v1/jobs/$job" > "${TMPDIR:-/tmp}/devassist-job.json"
python3 - "${TMPDIR:-/tmp}/devassist-job.json" <<'PY'
import json, sys

d = json.load(open(sys.argv[1]))
print()
for s in d["steps"]:
    print(f"  {s['sequence']:>2}  iter {s['iteration']}  {s['agent']:<9} {s['status']}")
for p in d["patches"]:
    v = p["validations"][-1] if p["validations"] else {}
    print(
        f"  patch {p['iteration']}: {p['status']:<10} tests={v.get('tests_status')} "
        f"security={v.get('security_status')} static={v.get('static_status')}"
    )
if d["review"]:
    print(f"\n  review ({d['review']['risk_level']} risk): {d['review']['summary']}")
print(f"\n  result: {d['status']}" + (f" - {d['error']}" if d["error"] else ""))
PY
echo
echo "Approve:  curl -X POST $API/v1/jobs/$job/approve"
echo "Details:  $API/v1/jobs/$job"
