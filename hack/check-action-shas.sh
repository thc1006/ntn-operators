#!/usr/bin/env bash
#
# check-action-shas.sh — verify every `uses: org/repo@<ref>` in
# .github/workflows/ is SHA-pinned, and (when a GitHub token is
# available) that the SHA resolves to a real commit on the action's
# repo.
#
# Motivation: PR #97 shipped three typo'd action SHAs that passed
# human review because they're opaque 40-char hex. Three follow-up
# PRs (#98) were needed to fix them. This script catches that class
# of error before merge.
#
# Checks (errors — fail run):
#   1. `uses: foo@<ref>` must be a 40-char lowercase hex SHA.
#      Semver tags (v1, v1.2.3, main, master) are rejected.
#   2. When GH_TOKEN or GITHUB_TOKEN is set, the SHA must resolve
#      via `gh api repos/<owner>/<repo>/commits/<sha>`.
#
# Checks (warnings — do NOT fail run):
#   3. Each SHA-pinned ref should have a trailing `# v<tag>` comment
#      for human auditability.
#
# Exempt:
#   - Local actions (`uses: ./...`).
#   - Docker-image actions (`uses: docker://...`).
#
# Usage:
#   hack/check-action-shas.sh [workflows_dir]
#
# Default workflows_dir is `.github/workflows`.

set -euo pipefail

WORKFLOWS_DIR="${1:-.github/workflows}"

if [ ! -d "${WORKFLOWS_DIR}" ]; then
  echo "error: workflows dir not found: ${WORKFLOWS_DIR}" >&2
  exit 2
fi

ERRORS=0
CHECKED=0
API_CHECKS=0
TOKEN="${GH_TOKEN:-${GITHUB_TOKEN:-}}"

# Collect all `uses: ...` lines across *.yml (one per line). Matches both
# block-style (`    uses: foo@sha`) and one-line step form
# (`  - uses: foo@sha`). The `|| true` is required because `grep` returns
# exit code 1 on zero matches, and `set -e` above would otherwise abort
# the script on an empty workflows dir — valid state we want to handle
# as "checked 0, exit 0".
# Format of `grep -nH -E` is `FILE:LINENUM:CONTENT`.
while IFS= read -r raw; do
  FILE="${raw%%:*}"
  REST="${raw#*:}"
  LINE_NO="${REST%%:*}"
  CONTENT="${REST#*:}"

  # Extract the uses value (everything after `uses:` up to whitespace
  # or comment marker). Then normalise optional surrounding YAML
  # quotes for scalars like `uses: "org/repo@<sha>"`.
  USES=$(printf '%s' "${CONTENT}" | sed -nE 's|.*uses:[[:space:]]+([^[:space:]#]+).*|\1|p')
  if [ -z "${USES}" ]; then
    continue
  fi
  case "${USES}" in
    \"*\") USES="${USES#\"}"; USES="${USES%\"}" ;;
    \'*\') USES="${USES#\'}"; USES="${USES%\'}" ;;
  esac

  # Skip local and docker actions.
  case "${USES}" in
    ./*|docker://*) continue ;;
  esac

  # Split on the last `@`.
  REF="${USES%@*}"
  SHA="${USES##*@}"

  if [ "${REF}" = "${USES}" ] || [ -z "${SHA}" ]; then
    echo "::error file=${FILE},line=${LINE_NO}::Action \`${USES}\` is missing an @<ref> pin."
    ERRORS=$((ERRORS + 1))
    continue
  fi

  CHECKED=$((CHECKED + 1))

  # 1. SHA format check (40 lowercase hex).
  if ! printf '%s' "${SHA}" | grep -qE '^[0-9a-f]{40}$'; then
    echo "::error file=${FILE},line=${LINE_NO}::Action \`${REF}\` is not SHA-pinned (ref=\`${SHA}\`). Pin to a 40-char hex commit SHA."
    ERRORS=$((ERRORS + 1))
    continue
  fi

  # 2. API resolution (only when token available).
  if [ -n "${TOKEN}" ]; then
    # Reuse repo slug: owner/repo (strip trailing /subpath for
    # actions like anchore/sbom-action/download-syft).
    REPO="$(printf '%s' "${REF}" | awk -F/ '{print $1"/"$2}')"
    if ! GH_TOKEN="${TOKEN}" gh api "repos/${REPO}/commits/${SHA}" --jq '.sha' >/dev/null 2>&1; then
      echo "::error file=${FILE},line=${LINE_NO}::Action \`${REF}@${SHA}\` does not resolve to a real commit on repo \`${REPO}\`."
      ERRORS=$((ERRORS + 1))
      continue
    fi
    API_CHECKS=$((API_CHECKS + 1))
  fi

  # 3. Auditability: expect a `# v<something>` comment trailing.
  if ! printf '%s' "${CONTENT}" | grep -qE '#[[:space:]]*v[0-9]'; then
    echo "::warning file=${FILE},line=${LINE_NO}::Action \`${REF}@${SHA}\` is missing a trailing \`# v<tag>\` comment (auditability only — not a failure)."
  fi
done < <(grep -nH -E '^[[:space:]]*-?[[:space:]]*uses:' "${WORKFLOWS_DIR}"/*.yml 2>/dev/null || true)

echo ""
echo "Checked ${CHECKED} action references across ${WORKFLOWS_DIR}/*.yml"
if [ -n "${TOKEN}" ]; then
  echo "  - SHA format: ${CHECKED} checked"
  echo "  - GitHub API resolution: ${API_CHECKS} verified"
else
  echo "  - SHA format: ${CHECKED} checked"
  echo "  - GitHub API resolution: SKIPPED (neither GH_TOKEN nor GITHUB_TOKEN set)"
fi

if [ "${ERRORS}" -gt 0 ]; then
  echo ""
  echo "✗ ${ERRORS} error(s) found."
  exit 1
fi

echo "✓ All action references valid."
