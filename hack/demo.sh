#!/usr/bin/env bash
# demo.sh — One-command demo of NTN K8s Operators
# Installs CRDs, applies all 4 sample resources, and watches status.
set -euo pipefail

SAMPLES_DIR="config/samples"

echo "=== NTN K8s Operators Demo ==="
echo ""

echo "1. Installing CRDs..."
make install
echo ""

echo "2. Starting controller (background)..."
setsid make run &
CONTROLLER_PID=$!
sleep 5
echo "   Controller process group PID: $CONTROLLER_PID"
echo ""

cleanup() {
    echo ""
    echo "=== Cleaning up ==="
    kill -- -"$CONTROLLER_PID" 2>/dev/null || true
    kubectl delete -f "$SAMPLES_DIR/ntn_v1alpha1_ntnslice.yaml" --ignore-not-found 2>/dev/null
    kubectl delete -f "$SAMPLES_DIR/ntn_v1alpha1_ntncellconfig.yaml" --ignore-not-found 2>/dev/null
    kubectl delete -f "$SAMPLES_DIR/ntn_v1alpha1_groundstationlifecycle_hsinchu.yaml" --ignore-not-found 2>/dev/null
    kubectl delete -f "$SAMPLES_DIR/ntn_v1alpha1_groundstationlifecycle.yaml" --ignore-not-found 2>/dev/null
    kubectl delete -f "$SAMPLES_DIR/ntn_v1alpha1_satelliteephemeris.yaml" --ignore-not-found 2>/dev/null
    echo "Done."
}
trap cleanup EXIT

echo "3. Creating SatelliteEphemeris (oneweb-constellation)..."
kubectl apply -f "$SAMPLES_DIR/ntn_v1alpha1_satelliteephemeris.yaml"
echo ""

echo "4. Creating GroundStationLifecycle (gs-taipei-01 + gs-hsinchu-01)..."
kubectl apply -f "$SAMPLES_DIR/ntn_v1alpha1_groundstationlifecycle.yaml"
kubectl apply -f "$SAMPLES_DIR/ntn_v1alpha1_groundstationlifecycle_hsinchu.yaml"
echo ""

echo "5. Creating NTNCellConfig (ntn-cell-geo-demo)..."
kubectl apply -f "$SAMPLES_DIR/ntn_v1alpha1_ntncellconfig.yaml"
echo ""

echo "6. Creating NTNSlice (enterprise-resilient-slice)..."
kubectl apply -f "$SAMPLES_DIR/ntn_v1alpha1_ntnslice.yaml"
echo ""

echo "7. Waiting for reconciliation..."
for i in $(seq 1 30); do
    count=$(kubectl get sateph oneweb-constellation -o jsonpath='{.status.satelliteCount}' 2>/dev/null || echo "")
    if [ -n "$count" ] && [ "$count" != "0" ]; then
        echo "   SatelliteEphemeris reconciled after $((i * 2))s (satelliteCount=$count)"
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo "   Timeout waiting for SatelliteEphemeris (CelesTrak may be rate-limited)"
    fi
    sleep 2
done

# Check NTNCellConfig
for i in $(seq 1 15); do
    cmref=$(kubectl get ntncellconfigs ntn-cell-geo-demo -o jsonpath='{.status.configMapRef}' 2>/dev/null || echo "")
    if [ -n "$cmref" ]; then
        echo "   NTNCellConfig reconciled (ConfigMap=$cmref)"
        break
    fi
    if [ "$i" -eq 15 ]; then
        echo "   NTNCellConfig pending"
    fi
    sleep 2
done

# Check NTNSlice
active=$(kubectl get ntnslices enterprise-resilient-slice -o jsonpath='{.status.activePathType}' 2>/dev/null || echo "")
if [ -n "$active" ]; then
    echo "   NTNSlice active path: $active"
fi

echo ""
echo "=== Results ==="
echo ""
echo "--- SatelliteEphemeris ---"
kubectl get sateph -o wide
echo ""
echo "--- GroundStationLifecycle ---"
kubectl get gs -o wide
echo ""
echo "--- NTNCellConfig ---"
kubectl get ntncellconfigs -o wide
echo ""
echo "--- NTNSlice ---"
kubectl get ntnslices -o wide
echo ""

echo "--- SatelliteEphemeris Conditions ---"
kubectl get sateph oneweb-constellation -o jsonpath='{range .status.conditions[*]}{.type}: {.status} ({.reason}) - {.message}{"\n"}{end}' 2>/dev/null || true
echo ""

echo "--- NTNCellConfig Status ---"
kubectl get ntncellconfigs ntn-cell-geo-demo -o jsonpath='ConfigMap: {.status.configMapRef}, Koffset: {.status.appliedKoffset}' 2>/dev/null || true
echo ""
echo ""

echo "--- NTNSlice Status ---"
kubectl get ntnslices enterprise-resilient-slice -o jsonpath='Active: {.status.activePathType}, Failovers: {.status.failoverCount}' 2>/dev/null || true
echo ""
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

echo "--- Recent Events ---"
kubectl get events --sort-by=.lastTimestamp --field-selector involvedObject.kind=SatelliteEphemeris 2>/dev/null | tail -5 || true
kubectl get events --sort-by=.lastTimestamp --field-selector involvedObject.kind=NTNCellConfig 2>/dev/null | tail -3 || true
kubectl get events --sort-by=.lastTimestamp --field-selector involvedObject.kind=NTNSlice 2>/dev/null | tail -3 || true
echo ""

echo "Press Ctrl+C to stop the demo."
wait "$CONTROLLER_PID" || true
