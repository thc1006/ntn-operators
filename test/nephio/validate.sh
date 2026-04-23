#!/usr/bin/env bash
# Validate the Nephio R6 kpt packages under nephio/packages/.
#
# This script is the executable contract for ADR 0003. It must stay fast
# and hermetic so it can run in CI and locally without a live K8s cluster.
#
# Exit codes:
#   0 = all tests passed
#   1 = one or more tests failed
#
# Usage:
#   test/nephio/validate.sh              # run all tests
#   test/nephio/validate.sh --verbose    # print rendered output on failure

set -o pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PKG_DIR="${REPO_ROOT}/nephio/packages"
CRDS_PKG="${PKG_DIR}/ntn-operators-crds"
WORKLOADS_PKG="${PKG_DIR}/ntn-workloads-sample"
CRD_BASES_DIR="${REPO_ROOT}/config/crd/bases"

# README-promised filenames (must match nephio/packages/*/README.md tables)
README_CRDS_FILES=(
  "satelliteephemeris-crd.yaml"
  "groundstationlifecycles-crd.yaml"
  "ntncellconfigs-crd.yaml"
  "ntnslices-crd.yaml"
)
README_SAMPLE_FILES=(
  "satelliteephemeris-sample.yaml"
  "groundstationlifecycle-sample.yaml"
  "ntncellconfig-sample.yaml"
  "ntnslice-sample.yaml"
)

EXPECTED_CRDS=(
  "satelliteephemeris"
  "groundstationlifecycle"
  "ntncellconfig"
  "ntnslice"
)

EXPECTED_SAMPLE_KINDS=(
  "SatelliteEphemeris"
  "GroundStationLifecycle"
  "NTNCellConfig"
  "NTNSlice"
)

PASS=0
FAIL=0
VERBOSE="${1:-}"

_ok()   { echo "  PASS  $1"; PASS=$((PASS+1)); }
_fail() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
_info() { echo "  ----  $1"; }

# kpt is required to be on $PATH. The canonical install path is
# $(go env GOPATH)/bin, which `make nephio-install-tools` puts on $PATH.
_has_kpt()    { command -v kpt >/dev/null 2>&1; }
_has_kubectl(){ command -v kubectl >/dev/null 2>&1; }

###############################################################################
echo "===> Preflight"
###############################################################################

if _has_kpt; then
  kpt_ver=$(kpt version 2>/dev/null | head -1)
  _ok "kpt CLI available (${kpt_ver})"
else
  _fail "kpt CLI not installed; run 'make nephio-install-tools'"
  echo
  echo "ABORT: cannot run without kpt."
  exit 1
fi

if _has_kubectl; then
  kc_ver=$(kubectl version --client 2>/dev/null | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1)
  _ok "kubectl available (${kc_ver})"
else
  _info "kubectl missing — skipping dry-run apply tests (T7, T12)"
fi

###############################################################################
echo
echo "===> Suite A: ntn-operators-crds package"
###############################################################################

# T1: package dir exists
if [ -d "$CRDS_PKG" ]; then
  _ok "T1 package dir exists: nephio/packages/ntn-operators-crds/"
else
  _fail "T1 package dir missing: nephio/packages/ntn-operators-crds/"
fi

# T2: Kptfile present + kpt-parseable
if [ -f "$CRDS_PKG/Kptfile" ]; then
  if kpt pkg tree "$CRDS_PKG" >/dev/null 2>&1; then
    _ok "T2 Kptfile valid, 'kpt pkg tree' succeeds"
  else
    _fail "T2 Kptfile present but 'kpt pkg tree' failed"
    [ "$VERBOSE" = "--verbose" ] && kpt pkg tree "$CRDS_PKG" 2>&1 | sed 's/^/       /'
  fi
else
  _fail "T2 Kptfile missing in $CRDS_PKG"
fi

# T3: kpt fn render succeeds on CRDs package
# Run on a temp copy so source tree stays pristine (kpt adds a status block
# and re-wraps long YAML strings — we do not want those committed).
if [ -f "$CRDS_PKG/Kptfile" ]; then
  tmpdir=$(mktemp -d)
  cp -r "$CRDS_PKG"/. "$tmpdir/"
  kpt fn render "$tmpdir" >/dev/null 2>&1
  rc=$?
  if [ $rc -eq 0 ]; then
    _ok "T3 'kpt fn render' succeeds on CRDs package"
  else
    _fail "T3 'kpt fn render' failed on CRDs package (exit $rc)"
    [ "$VERBOSE" = "--verbose" ] && kpt fn render "$tmpdir" 2>&1 | sed 's/^/       /'
  fi
  rm -rf "$tmpdir"
else
  _fail "T3 skipped (Kptfile missing)"
fi

# T4: all README-promised CRD filenames exist on disk (catches filename drift)
missing=()
for fname in "${README_CRDS_FILES[@]}"; do
  [ -f "$CRDS_PKG/$fname" ] || missing+=("$fname")
done
if [ ${#missing[@]} -eq 0 ]; then
  _ok "T4 all 4 README-promised CRD filenames present (filename contract honored)"
else
  _fail "T4 missing README-promised files: ${missing[*]}"
fi

# T5: package content declares 4 CustomResourceDefinition kinds
if [ -f "$CRDS_PKG/Kptfile" ]; then
  crd_count=$(grep -c "^kind: CustomResourceDefinition$" "$CRDS_PKG"/*.yaml 2>/dev/null | awk -F: '{sum+=$2} END {print sum+0}')
  if [ "$crd_count" -eq 4 ]; then
    _ok "T5 exactly 4 CustomResourceDefinition kinds in package"
  else
    _fail "T5 expected 4 CRD kinds, found $crd_count"
  fi
else
  _fail "T5 skipped (Kptfile missing)"
fi

# T6: README.md present
if [ -f "$CRDS_PKG/README.md" ]; then
  _ok "T6 README.md present"
else
  _fail "T6 README.md missing in $CRDS_PKG"
fi

# T7: rendered CRDs pass kubectl client-side dry-run (on temp copy, source stays clean)
if [ -f "$CRDS_PKG/Kptfile" ] && _has_kubectl; then
  tmpdir=$(mktemp -d)
  cp -r "$CRDS_PKG"/. "$tmpdir/"
  kpt fn render "$tmpdir" >/dev/null 2>&1
  crd_files=$(ls "$tmpdir"/*-crd.yaml 2>/dev/null)
  if [ -n "$crd_files" ]; then
    cat "$tmpdir"/*-crd.yaml | kubectl apply --dry-run=client -f - >/dev/null 2>&1
    rc=$?
    if [ $rc -eq 0 ]; then
      _ok "T7 rendered CRDs pass 'kubectl apply --dry-run=client'"
    else
      _fail "T7 rendered CRDs failed 'kubectl apply --dry-run=client' (exit $rc)"
      if [ "$VERBOSE" = "--verbose" ]; then
        cat "$tmpdir"/*-crd.yaml | kubectl apply --dry-run=client -f - 2>&1 | sed 's/^/       /' | head -20
      fi
    fi
  else
    _fail "T7 no -crd.yaml files found in temp copy of $CRDS_PKG"
  fi
  rm -rf "$tmpdir"
fi

# T13: CRD drift detection — package copies must byte-match the controller-gen source
#       (config/crd/bases/*.yaml is the single source of truth per ADR 0003)
drift=0
drift_files=()
for fname in "${README_CRDS_FILES[@]}"; do
  # map package name back to base name:
  #   satelliteephemeris-crd.yaml -> ntn.operators.dev_satelliteephemeris.yaml
  #   groundstationlifecycles-crd.yaml -> ntn.operators.dev_groundstationlifecycles.yaml
  kind_plural=$(echo "$fname" | sed 's/-crd\.yaml$//')
  base="$CRD_BASES_DIR/ntn.operators.dev_${kind_plural}.yaml"
  pkg_file="$CRDS_PKG/$fname"
  if [ ! -f "$base" ]; then
    drift=1
    drift_files+=("${fname}: source $(basename "$base") not found in config/crd/bases/")
    continue
  fi
  if ! diff -q "$base" "$pkg_file" >/dev/null 2>&1; then
    drift=1
    drift_files+=("${fname}: differs from config/crd/bases/$(basename "$base")")
  fi
done
if [ $drift -eq 0 ]; then
  _ok "T13 CRD drift check passes (package matches config/crd/bases/)"
else
  _fail "T13 CRD drift detected:"
  for msg in "${drift_files[@]}"; do
    _fail "      $msg"
    PASS=$((PASS+1))  # undo the extra FAIL increment from _fail inside loop
    FAIL=$((FAIL-1))
  done
fi

###############################################################################
echo
echo "===> Suite B: ntn-workloads-sample package"
###############################################################################

# T8: package dir exists
if [ -d "$WORKLOADS_PKG" ]; then
  _ok "T8 package dir exists: nephio/packages/ntn-workloads-sample/"
else
  _fail "T8 package dir missing: nephio/packages/ntn-workloads-sample/"
fi

# T9: Kptfile present + kpt-parseable
if [ -f "$WORKLOADS_PKG/Kptfile" ]; then
  if kpt pkg tree "$WORKLOADS_PKG" >/dev/null 2>&1; then
    _ok "T9 Kptfile valid, 'kpt pkg tree' succeeds"
  else
    _fail "T9 Kptfile present but 'kpt pkg tree' failed"
  fi
else
  _fail "T9 Kptfile missing in $WORKLOADS_PKG"
fi

# T10: kpt fn render succeeds and pipeline mutates (namespace must change to ntn-system)
if [ -f "$WORKLOADS_PKG/Kptfile" ]; then
  # Work on a temp copy so the test is idempotent (does not mutate the tree
  # whether it starts in pre- or post-rendered state).
  tmpdir=$(mktemp -d)
  cp -r "$WORKLOADS_PKG"/. "$tmpdir/"
  kpt fn render "$tmpdir" >/dev/null 2>&1
  rc=$?
  if [ $rc -eq 0 ] && grep -q "namespace: ntn-system" "$tmpdir"/*.yaml 2>/dev/null; then
    _ok "T10 'kpt fn render' succeeds + set-namespace mutator applied (namespace: ntn-system)"
  else
    _fail "T10 render failed or set-namespace mutator did not apply (exit $rc)"
    if [ "$VERBOSE" = "--verbose" ]; then
      grep -H namespace "$tmpdir"/*.yaml 2>/dev/null | sed 's/^/       /' | head -10
    fi
  fi
  rm -rf "$tmpdir"
fi

# T11: all 4 README-promised sample filenames exist on disk
missing=()
for fname in "${README_SAMPLE_FILES[@]}"; do
  [ -f "$WORKLOADS_PKG/$fname" ] || missing+=("$fname")
done
if [ ${#missing[@]} -eq 0 ]; then
  _ok "T11 all 4 README-promised sample filenames present (filename contract honored)"
else
  _fail "T11 missing README-promised files: ${missing[*]}"
fi

# T14: all 4 sample kinds present (content check)
if [ ${#missing[@]} -eq 0 ]; then
  missing_kinds=()
  for kind in "${EXPECTED_SAMPLE_KINDS[@]}"; do
    if ! grep -q "^kind: ${kind}$" "$WORKLOADS_PKG"/*-sample.yaml 2>/dev/null; then
      missing_kinds+=("$kind")
    fi
  done
  if [ ${#missing_kinds[@]} -eq 0 ]; then
    _ok "T14 all 4 sample CR kinds present in YAML content"
  else
    _fail "T14 missing sample kinds: ${missing_kinds[*]}"
  fi
fi

# T12: rendered workloads parse as valid K8s YAML (CRDs may not be installed locally)
if [ -f "$WORKLOADS_PKG/Kptfile" ] && _has_kubectl; then
  tmpdir=$(mktemp -d)
  cp -r "$WORKLOADS_PKG"/. "$tmpdir/"
  kpt fn render "$tmpdir" >/dev/null 2>&1
  sample_files=$(ls "$tmpdir"/*-sample.yaml 2>/dev/null)
  if [ -n "$sample_files" ]; then
    cat "$tmpdir"/*-sample.yaml | kubectl apply --dry-run=client --validate=false -f - >/dev/null 2>&1
    rc=$?
    if [ $rc -eq 0 ]; then
      _ok "T12 rendered samples parse as valid K8s YAML"
    else
      _fail "T12 rendered samples failed YAML parse (exit $rc)"
      if [ "$VERBOSE" = "--verbose" ]; then
        cat "$tmpdir"/*-sample.yaml | kubectl apply --dry-run=client --validate=false -f - 2>&1 | sed 's/^/       /' | head -20
      fi
    fi
  else
    _fail "T12 no rendered *-sample.yaml files"
  fi
  rm -rf "$tmpdir"
fi

###############################################################################
echo
echo "===> Summary"
###############################################################################

TOTAL=$((PASS+FAIL))
echo "  Passed: $PASS / $TOTAL"
echo "  Failed: $FAIL / $TOTAL"

if [ "$FAIL" -eq 0 ]; then
  echo
  echo "OK: all Nephio package assertions pass."
  exit 0
else
  echo
  echo "FAIL: $FAIL assertion(s) failed. Re-run with --verbose for diagnostics."
  exit 1
fi
