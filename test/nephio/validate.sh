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
# NB: T7 and T12 used to require kubectl for client-side dry-run apply.
# That depended on cluster API discovery, which fails on hermetic CI
# runners with no kubeconfig. Both tests now use python3 yaml.safe_load_all
# with structural shape checks — kubectl is no longer required.

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

if command -v python3 >/dev/null 2>&1; then
  py_ver=$(python3 --version 2>&1 | head -1)
  _ok "python3 available (${py_ver}) — required for T7/T12 structural YAML checks"
else
  _fail "python3 not on PATH — T7/T12 cannot run; install python3"
  echo
  echo "ABORT: cannot run without python3."
  exit 1
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

# T7: rendered CRDs are well-formed CustomResourceDefinition YAML
#     (hermetic — uses python3 yaml.safe_load_all and a structural shape
#      check). kubectl was abandoned here because `kubectl apply
#      --dry-run=client` performs cluster API discovery even with
#      --validate=false, which fails on CI runners that have no
#      kubeconfig context (#119 first-run failure).
if [ -f "$CRDS_PKG/Kptfile" ]; then
  tmpdir=$(mktemp -d)
  cp -r "$CRDS_PKG"/. "$tmpdir/"
  kpt fn render "$tmpdir" >/dev/null 2>&1
  crd_files=$(ls "$tmpdir"/*-crd.yaml 2>/dev/null)
  if [ -n "$crd_files" ]; then
    cat > "$tmpdir/_check.py" <<'PY'
import sys, yaml
errs = []
for i, doc in enumerate(yaml.safe_load_all(sys.stdin)):
    if doc is None:
        continue
    if not isinstance(doc, dict):
        errs.append(f"doc {i}: not a YAML mapping")
        continue
    if doc.get("kind") != "CustomResourceDefinition":
        errs.append(f"doc {i}: kind={doc.get('kind')!r}, expected CustomResourceDefinition")
    spec = doc.get("spec") or {}
    for required in ("group", "names", "scope", "versions"):
        if required not in spec:
            errs.append(f"doc {i}: missing required spec.{required}")
for e in errs:
    print(e, file=sys.stderr)
sys.exit(1 if errs else 0)
PY
    cat "$tmpdir"/*-crd.yaml | python3 "$tmpdir/_check.py" >/dev/null 2>"$tmpdir/.err"
    rc=$?
    if [ $rc -eq 0 ]; then
      _ok "T7 rendered CRDs are well-formed CustomResourceDefinition YAML"
    else
      _fail "T7 rendered CRDs failed structural validation (exit $rc)"
      if [ "$VERBOSE" = "--verbose" ] && [ -f "$tmpdir/.err" ]; then
        sed 's/^/       /' "$tmpdir/.err" | head -20
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

# T12: rendered samples parse as valid YAML and declare an expected NTN kind
#      (hermetic — same kubectl-vs-API-discovery rationale as T7).
if [ -f "$WORKLOADS_PKG/Kptfile" ]; then
  tmpdir=$(mktemp -d)
  cp -r "$WORKLOADS_PKG"/. "$tmpdir/"
  kpt fn render "$tmpdir" >/dev/null 2>&1
  sample_files=$(ls "$tmpdir"/*-sample.yaml 2>/dev/null)
  if [ -n "$sample_files" ]; then
    EXPECTED_KINDS_CSV=$(IFS=,; echo "${EXPECTED_SAMPLE_KINDS[*]}")
    cat > "$tmpdir/_check.py" <<'PY'
import os, sys, yaml
expected = set(os.environ["EXPECTED_KINDS"].split(","))
errs = []
for i, doc in enumerate(yaml.safe_load_all(sys.stdin)):
    if doc is None:
        continue
    if not isinstance(doc, dict):
        errs.append(f"doc {i}: not a YAML mapping")
        continue
    api = doc.get("apiVersion")
    kind = doc.get("kind")
    if api != "ntn.operators.dev/v1alpha1":
        errs.append(f"doc {i}: apiVersion={api!r}, expected ntn.operators.dev/v1alpha1")
    if kind not in expected:
        errs.append(f"doc {i}: kind={kind!r}, not in expected {sorted(expected)}")
    if "metadata" not in doc or not isinstance(doc.get("metadata"), dict):
        errs.append(f"doc {i}: missing metadata")
for e in errs:
    print(e, file=sys.stderr)
sys.exit(1 if errs else 0)
PY
    cat "$tmpdir"/*-sample.yaml | EXPECTED_KINDS="$EXPECTED_KINDS_CSV" python3 "$tmpdir/_check.py" >/dev/null 2>"$tmpdir/.err"
    rc=$?
    if [ $rc -eq 0 ]; then
      _ok "T12 rendered samples are well-formed NTN CR YAML"
    else
      _fail "T12 rendered samples failed structural validation (exit $rc)"
      if [ "$VERBOSE" = "--verbose" ] && [ -f "$tmpdir/.err" ]; then
        sed 's/^/       /' "$tmpdir/.err" | head -20
      fi
    fi
  else
    _fail "T12 no rendered *-sample.yaml files"
  fi
  rm -rf "$tmpdir"
fi

###############################################################################
echo
echo "===> Suite C: supply-chain"
###############################################################################

# T15: every Kptfile pipeline mutator image must be pinned by @sha256 digest.
# Delegates to hack/check-kptfile-digest-pin.sh (mirrors the action-SHA
# pattern in hack/check-action-shas.sh, #109). Tag pinning is rejected
# because GHCR/OCI tags are mutable (#114).
DIGEST_CHECK="${REPO_ROOT}/hack/check-kptfile-digest-pin.sh"
if [ -x "$DIGEST_CHECK" ]; then
  if "$DIGEST_CHECK" "$PKG_DIR" >/dev/null 2>&1; then
    _ok "T15 all Kptfile pipeline images pinned by @sha256 digest"
  else
    _fail "T15 Kptfile pipeline images NOT pinned by @sha256 digest"
    if [ "$VERBOSE" = "--verbose" ]; then
      "$DIGEST_CHECK" "$PKG_DIR" 2>&1 | sed 's/^/       /'
    fi
  fi
else
  _fail "T15 hack/check-kptfile-digest-pin.sh not found or not executable"
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
