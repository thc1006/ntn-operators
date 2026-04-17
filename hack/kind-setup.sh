#!/usr/bin/env bash
# kind-setup.sh — One-command demo cluster for NTN K8s Operators
#
# Usage:
#   hack/kind-setup.sh          # create cluster + install + demo
#   hack/kind-setup.sh teardown # delete the cluster
#
set -euo pipefail

CLUSTER_NAME="${KIND_CLUSTER:-ntn-demo}"
IMG="${IMG:-ghcr.io/thc1006/ntn-operators:dev}"
SAMPLES_DIR="config/samples"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

# ── Teardown ──────────────────────────────────────────────────
if [ "${1:-}" = "teardown" ]; then
    echo "Deleting Kind cluster '$CLUSTER_NAME'..."
    kind delete cluster --name "$CLUSTER_NAME"
    echo "Done."
    exit 0
fi

# ── Prerequisites ─────────────────────────────────────────────
for cmd in kind kubectl make go; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "ERROR: $cmd is required but not found." >&2
        exit 1
    fi
done

echo "=== NTN Operators Kind Demo ==="
echo "Cluster: $CLUSTER_NAME"
echo "Image:   $IMG"
echo ""

# ── 1. Create Kind cluster ───────────────────────────────────
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
    echo "1. Kind cluster '$CLUSTER_NAME' already exists, reusing."
else
    echo "1. Creating Kind cluster '$CLUSTER_NAME'..."
    kind create cluster --name "$CLUSTER_NAME" --wait 60s
fi
echo ""

# ── 2. Build and load image ──────────────────────────────────
echo "2. Building container image..."
# Use docker if available, fallback to nerdctl/podman
CONTAINER_TOOL="docker"
if ! command -v docker &>/dev/null; then
    if command -v nerdctl &>/dev/null; then
        CONTAINER_TOOL="nerdctl"
    elif command -v podman &>/dev/null; then
        CONTAINER_TOOL="podman"
    else
        echo "ERROR: No container tool found (docker/nerdctl/podman)." >&2
        exit 1
    fi
fi

$CONTAINER_TOOL build -t "$IMG" .
echo ""

echo "   Loading image into Kind..."
if [ "$CONTAINER_TOOL" = "docker" ]; then
    kind load docker-image "$IMG" --name "$CLUSTER_NAME"
else
    # nerdctl/podman: export to tarball then load via archive
    TMPTAR="$(mktemp /tmp/ntn-image-XXXXXX.tar)"
    $CONTAINER_TOOL save -o "$TMPTAR" "$IMG"
    kind load image-archive "$TMPTAR" --name "$CLUSTER_NAME"
    rm -f "$TMPTAR"
fi
echo ""

# ── 3. Install CRDs ──────────────────────────────────────────
echo "3. Installing CRDs..."
make install
echo ""

# ── 4. Deploy via Helm ────────────────────────────────────────
echo "4. Deploying operator via Helm..."
if ! command -v helm &>/dev/null; then
    echo "   Helm not found, deploying via make deploy..."
    make deploy IMG="$IMG"
else
    # Uninstall previous release if exists
    helm uninstall ntn-operators -n ntn-operators-system 2>/dev/null || true
    helm install ntn-operators dist/chart \
        --namespace ntn-operators-system \
        --create-namespace \
        --set manager.image.repository="${IMG%%:*}" \
        --set manager.image.tag="${IMG##*:}" \
        --set manager.image.pullPolicy=Never \
        --set crd.enable=false \
        --wait --timeout 3m
fi
echo ""

# ── 5. Apply samples ─────────────────────────────────────────
echo "5. Applying sample resources..."
kubectl apply -f "$SAMPLES_DIR/ntn_v1alpha1_satelliteephemeris.yaml"
kubectl apply -f "$SAMPLES_DIR/ntn_v1alpha1_groundstationlifecycle.yaml"
kubectl apply -f "$SAMPLES_DIR/ntn_v1alpha1_groundstationlifecycle_hsinchu.yaml"
kubectl apply -f "$SAMPLES_DIR/ntn_v1alpha1_ntncellconfig.yaml"
kubectl apply -f "$SAMPLES_DIR/ntn_v1alpha1_ntnslice.yaml"
echo ""

# ── 6. Wait and show status ──────────────────────────────────
echo "6. Waiting for reconciliation (up to 60s)..."
for i in $(seq 1 30); do
    count=$(kubectl get sateph oneweb-constellation -o jsonpath='{.status.satelliteCount}' 2>/dev/null || echo "")
    if [ -n "$count" ] && [ "$count" != "0" ]; then
        echo "   SatelliteEphemeris ready (satelliteCount=$count)"
        break
    fi
    sleep 2
done
echo ""

echo "=== Status ==="
echo ""
kubectl get sateph,gs,ntncellconfigs,ntnslices -o wide 2>/dev/null || true
echo ""
kubectl get pods -n ntn-operators-system
echo ""

echo "=== Demo Ready ==="
echo ""
echo "To tear down: hack/kind-setup.sh teardown"
echo "To access:    kubectl get sateph,gs,ntncellconfigs,ntnslices"
