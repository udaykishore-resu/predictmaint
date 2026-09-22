#!/usr/bin/env bash
# End-to-end demo against a running predictmaint (default: make run on :8080).
# Shows: anomaly rising on one of five pumps, ONE alert (not fifty), ONE work
# order, and the feedback loop moving the asset's threshold.
set -euo pipefail
URL="${PREDICTMAINT_URL:-http://localhost:8080}"
HERE="$(cd "$(dirname "$0")" && pwd)"
SIM="${SIMULATOR_BIN:-$HERE/../bin/simulator}"
json() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(json.dumps(d, indent=2))'; }
field() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }

echo "== 1. liveness / readiness"
curl -fsS "$URL/healthz"; echo
curl -fsS "$URL/readyz"; echo

echo
echo "== 2. one hand-written batch (unit-safe: F, psi are converted to the template's C, bar)"
curl -fsS -X POST "$URL/v1/metrics" -H 'content-type: application/json' --data @"$HERE/metrics_batch.json"; echo

echo
echo "== 3. stream the 5-pump fleet; pump-003 develops an outer-race bearing fault"
"$SIM" -url "$URL" -steps 160 -quiet

echo
echo "== 4. health of the degrading pump vs a healthy one"
curl -fsS "$URL/v1/assets/pump-003/health" | field '"pump-003 risk=%.3f state=%s baseline=%s threshold=%.2f top=%s" % (d["risk"], d["alert_state"], d["baseline_source"], d["effective_threshold"], d["top_contributors"][0]["feature"])'
curl -fsS "$URL/v1/assets/pump-001/health" | field '"pump-001 risk=%.3f state=%s baseline=%s" % (d["risk"], d["alert_state"], d["baseline_source"])'

echo
echo "== 5. alerts for the site: exactly one"
ALERT_ID=$(curl -fsS "$URL/v1/alerts?site=plant-a" | field 'd["alerts"][0]["id"]')
curl -fsS "$URL/v1/alerts?site=plant-a" | field '"count=%d asset=%s failure_mode=%s confidence=%.2f rules=%s" % (d["count"], d["alerts"][0]["asset_id"], d["alerts"][0]["failure_mode"], d["alerts"][0]["confidence"], ",".join(d["alerts"][0]["rules"]))'

echo
echo "== 6. the work order it raised (simulated CMMS; Maximo/SAP PM payloads rendered inside)"
curl -fsS "$URL/v1/workorders?asset_id=pump-003" | field '"count=%d id=%s external=%s status=%s" % (d["count"], d["work_orders"][0]["id"], d["work_orders"][0]["external"]["id"], d["work_orders"][0]["status"])'

echo
echo "== 7. technician confirms -> threshold tightens (ADAPT-1), MTTA recorded"
curl -fsS -X POST "$URL/v1/alerts/$ALERT_ID/feedback" -H 'content-type: application/json' --data @"$HERE/feedback_confirm.json" | field 'json.dumps(d["adaptation"])'
curl -fsS "$URL/v1/stats?site=plant-a" | json

echo
echo "== 8. tune the class template for this plant (validated, live, assets rebuilt)"
curl -fsS -X PUT "$URL/v1/templates/pump" -H 'content-type: application/json' --data @"$HERE/template_pump_tuned.json" | field '"pump template now version=%s on_threshold=%.2f" % (d["version"], d["alerting"]["on_threshold"])'
curl -fsS "$URL/v1/assets/pump-003/health" | field '"pump-003 template=%s effective_threshold=%.2f (0.65 - 0.02 confirm offset)" % (d["template_version"], d["effective_threshold"])'
echo
echo "done."
