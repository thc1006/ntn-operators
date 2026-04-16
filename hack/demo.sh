#!/usr/bin/env bash
# demo.sh — One-command demo of NTN K8s Operators
# Installs CRDs, applies samples, and watches status.
set -euo pipefail

echo "=== NTN K8s Operators Demo ==="
echo ""

echo "1. Installing CRDs..."
make install
echo ""

echo "2. Starting controller (background)..."
make run &
CONTROLLER_PID=$!
sleep 5
echo "   Controller PID: $CONTROLLER_PID"
echo ""

cleanup() {
    echo ""
    echo "=== Cleaning up ==="
    kill "$CONTROLLER_PID" 2>/dev/null || true
    kubectl delete -f config/samples/ntn_v1alpha1_satelliteephemeris.yaml --ignore-not-found 2>/dev/null
    kubectl delete -f config/samples/ntn_v1alpha1_groundstationlifecycle.yaml --ignore-not-found 2>/dev/null
    echo "Done."
}
trap cleanup EXIT

echo "3. Creating GroundStationLifecycle (gs-taipei-01)..."
kubectl apply -f config/samples/ntn_v1alpha1_groundstationlifecycle.yaml
echo ""

echo "4. Creating SatelliteEphemeris (oneweb-constellation)..."
kubectl apply -f config/samples/ntn_v1alpha1_satelliteephemeris.yaml
echo ""

echo "5. Waiting for reconciliation..."
for i in $(seq 1 30); do
    count=$(kubectl get sateph oneweb-constellation -o jsonpath='{.status.satelliteCount}' 2>/dev/null || echo "")
    if [ -n "$count" ] && [ "$count" != "0" ]; then
        echo "   Reconciled after $((i * 2))s (satelliteCount=$count)"
        break
    fi
    sleep 2
done

echo ""
echo "=== Results ==="
echo ""
echo "--- kubectl get sateph ---"
kubectl get sateph -o wide
echo ""
echo "--- kubectl get gs ---"
kubectl get gs -o wide
echo ""
echo "--- Conditions ---"
kubectl get sateph oneweb-constellation -o jsonpath='{range .status.conditions[*]}{.type}: {.status} ({.reason}) - {.message}{"\n"}{end}'
echo ""
echo "--- Pass Windows (first 5) ---"
kubectl get sateph oneweb-constellation -o json | python3 -c "
import sys, json
doc = json.load(sys.stdin)
data = doc.get('status', {}).get('nextPassWindows') or []
if not data:
    print('(no passes yet)')
    sys.exit(0)
print(f'Total: {len(data)} passes')
for p in data[:5]:
    print(f\"  {p['satellite']:20s} over {p['groundStation']:15s} AOS={p['aos'][:19]} LOS={p['los'][:19]} MaxEl={p['maxElevation']}°\")
if len(data) > 5:
    print(f'  ... and {len(data)-5} more')
" 2>/dev/null || echo "(no passes yet)"
echo ""
echo "--- Events ---"
kubectl describe sateph oneweb-constellation | grep -A10 "^Events:"
echo ""
echo "Press Ctrl+C to stop the demo."
wait "$CONTROLLER_PID"
