#!/bin/bash
# codex-usage-collect.sh — run on us-acq (root), every 5 min via
# codex-usage-collect.timer. Samples the ChatGPT/Codex quota with the
# codex-usage probe, writes JSON locally, and pushes it to bj-brain so
# the FleetMon server can serve it at /api/codex-usage and the
# dashboard can render the "Codex 额度" card.
# Push path: us-acq root -> ssh ubuntu@100.64.0.2 (mesh, key auth)
#            -> sudo install into /opt/fleetmon/codex-usage.json (644).
set -u
PROBE=/root/.venvs/codex-probe/bin/codex-usage
BJ=ubuntu@100.64.0.2
SSHOPTS="-o BatchMode=yes -o ConnectTimeout=15 -o StrictHostKeyChecking=accept-new"

# NOTE: the probe prints the full report but exits 1 anyway (observed
# 2026-11-20), so do not trust its exit code — the parser below is the
# real success check (it fails unless both windows are present).
OUT_TXT=$($PROBE status 2>/dev/null)
JSON=$(printf '%s' "$OUT_TXT" | python3 -c '
import sys, re, json, time
txt = sys.stdin.read()
def win(label):
    m = re.search(label + r" Window:\s*\n\s+Used:\s+([\d.]+)%[^\n]*\n\s+Remaining:\s+([\d.]+)%[^\n]*\n\s+Resets in:\s+([^\n]+)", txt)
    if not m:
        return None
    return {"used_pct": float(m.group(1)), "remaining_pct": float(m.group(2)),
            "resets_in": m.group(3).strip()}
w5, w7 = win("5h"), win("7d")
if not w5 or not w7:
    sys.exit(1)
plan = re.search(r"Plan:\s+(\S+)", txt)
print(json.dumps({"collected_at": int(time.time()),
                  "plan": plan.group(1) if plan else "",
                  "win5h": w5, "win7d": w7}))
') || { echo "parse failed" >&2; exit 1; }

printf '%s\n' "$JSON" > /root/codex-usage.json.tmp || exit 1
mv /root/codex-usage.json.tmp /root/codex-usage.json || exit 1

scp -q $SSHOPTS /root/codex-usage.json $BJ:/tmp/codex-usage.json.tmp || { echo "scp failed" >&2; exit 1; }
ssh $SSHOPTS $BJ "sudo install -m 644 /tmp/codex-usage.json.tmp /opt/fleetmon/codex-usage.json && rm -f /tmp/codex-usage.json.tmp" \
  || { echo "remote install failed" >&2; exit 1; }
