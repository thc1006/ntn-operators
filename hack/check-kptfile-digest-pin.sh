#!/usr/bin/env bash
#
# Requires bash 4+ (uses mapfile, [[ =~ ]], BASH_REMATCH). macOS users
# on the system bash 3.2 should `brew install bash` and re-run; Linux
# distros and CI runners ship bash 5+ by default.
#
# check-kptfile-digest-pin.sh — verify every Kptfile pipeline mutator
# AND validator image is pinned by @sha256 digest, not by mutable tag.
#
# Motivation: GHCR / OCI tags are mutable. A compromised upstream could
# silently repoint `set-namespace:v0.4.1` to a malicious image on the
# next `kpt fn render`. This script enforces digest pinning for the
# Nephio kpt packages we ship under nephio/packages/, mirroring the
# action-SHA pinning discipline established in hack/check-action-shas.sh
# (#107 / #109).
#
# Checks (errors — fail run):
#   1. Every `image:` field inside a Kptfile `pipeline:` section must
#      be of the form `<registry>/<path>@sha256:<64-hex>`. Tag pinning
#      (`:v1.2.3`, `:latest`) is rejected.
#
# Exempt:
#   - `image:` lines outside the pipeline section (none expected today,
#     but harmless if a future Kptfile adds metadata-side image refs).
#   - Kptfiles with no pipeline section at all (CRD-only packages).
#
# Usage:
#   hack/check-kptfile-digest-pin.sh [packages_dir]
#
# Default packages_dir is `nephio/packages`.

set -euo pipefail

PKGS_DIR="${1:-nephio/packages}"

if [ ! -d "${PKGS_DIR}" ]; then
  echo "error: packages dir not found: ${PKGS_DIR}" >&2
  exit 2
fi

ERRORS=0
CHECKED=0

# Collect all Kptfile paths. `|| true` because find returns 0 even when
# empty; mapfile would otherwise see no input. Sorted for deterministic
# error ordering across CI runs.
mapfile -t KPTFILES < <(find "${PKGS_DIR}" -type f -name Kptfile | sort)

if [ "${#KPTFILES[@]}" -eq 0 ]; then
  # No Kptfiles under a directory that exists is a setup error (e.g., the
  # packages tree was renamed but this script's default was not updated).
  # Treat as exit 2 to match the missing-dir branch above and avoid the
  # silent green of "0 errors checked".
  echo "error: no Kptfile found under ${PKGS_DIR}" >&2
  exit 2
fi

for KPT in "${KPTFILES[@]}"; do
  IN_PIPELINE=0
  LINE_NO=0
  while IFS= read -r LINE || [ -n "${LINE}" ]; do
    LINE_NO=$((LINE_NO + 1))

    # Enter pipeline block on a top-level `pipeline:` key.
    if [[ "${LINE}" =~ ^pipeline:[[:space:]]*$ ]]; then
      IN_PIPELINE=1
      continue
    fi
    # Exit pipeline block on any other top-level (column-0) key.
    if [ "${IN_PIPELINE}" -eq 1 ] && [[ "${LINE}" =~ ^[a-zA-Z] ]]; then
      IN_PIPELINE=0
    fi
    [ "${IN_PIPELINE}" -eq 0 ] && continue

    # Match `image: <ref>` (with optional leading list dash).
    if [[ "${LINE}" =~ ^[[:space:]]*-?[[:space:]]*image:[[:space:]]+([^[:space:]#]+) ]]; then
      IMG="${BASH_REMATCH[1]}"

      # Strip optional surrounding YAML quotes.
      DQ='"'
      SQ="'"
      if [ "${IMG#${DQ}}" != "${IMG}" ] && [ "${IMG%${DQ}}" != "${IMG}" ]; then
        IMG="${IMG#${DQ}}"; IMG="${IMG%${DQ}}"
      elif [ "${IMG#${SQ}}" != "${IMG}" ] && [ "${IMG%${SQ}}" != "${IMG}" ]; then
        IMG="${IMG#${SQ}}"; IMG="${IMG%${SQ}}"
      fi

      CHECKED=$((CHECKED + 1))

      # Require trailing `@sha256:<64-hex>`.
      if ! printf '%s' "${IMG}" | grep -qE '@sha256:[0-9a-f]{64}$'; then
        echo "::error file=${KPT},line=${LINE_NO}::Mutator image \`${IMG}\` is not pinned by digest. Pin to <registry>/<path>@sha256:<64-hex>."
        ERRORS=$((ERRORS + 1))
      fi
    fi
  done < "${KPT}"
done

echo ""
echo "Checked ${CHECKED} pipeline image reference(s) across ${#KPTFILES[@]} Kptfile(s) under ${PKGS_DIR}/"

if [ "${ERRORS}" -gt 0 ]; then
  echo ""
  echo "FAIL: ${ERRORS} error(s) found."
  exit 1
fi

echo "OK: all Kptfile pipeline images pinned by @sha256 digest."
