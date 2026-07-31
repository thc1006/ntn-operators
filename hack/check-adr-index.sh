#!/usr/bin/env bash
# Validate docs/adr/ integrity so a numbering collision (two ADR-0010s) or a
# dangling cross-reference cannot merge. Checks, per ADR file:
#   - the 4-digit number is unique across docs/adr/
#   - the "# ADR NNNN" title matches the filename number
#   - a "- Status:" line is present
#   - the ADR is listed in the index (docs/adr/README.md)
#   - every relative link to another ADR file resolves
set -euo pipefail

adr_dir="docs/adr"
readme="$adr_dir/README.md"
fail=0
err() { printf 'adr-lint: %s\n' "$*" >&2; fail=1; }

[[ -f "$readme" ]] || { printf 'adr-lint: missing index %s\n' "$readme" >&2; exit 1; }

shopt -s nullglob
declare -A seen
count=0
for f in "$adr_dir"/[0-9][0-9][0-9][0-9]-*.md; do
  count=$((count + 1))
  base=$(basename "$f")
  num=${base:0:4}

  if [[ -n "${seen[$num]:-}" ]]; then
    err "duplicate ADR number $num: ${seen[$num]} and $base"
  else
    seen[$num]=$base
  fi

  title_num=$(grep -m1 -oE '^# ADR [0-9]{4}' "$f" | grep -oE '[0-9]{4}' || true)
  [[ "$title_num" == "$num" ]] || err "$base: title number '${title_num:-none}' != filename number '$num'"

  grep -qE '^- Status:' "$f" || err "$base: missing '- Status:' line"

  grep -qE "(^|[^0-9])$num([^0-9]|\$)" "$readme" || err "$base: ADR $num not listed in $readme"

  while IFS= read -r target; do
    [[ -z "$target" ]] && continue
    [[ -f "$adr_dir/$target" ]] || err "$base: broken relative link -> $target"
  done < <(grep -oE '\]\(\.?/?[0-9]{4}-[a-z0-9-]+\.md' "$f" | grep -oE '[0-9]{4}-[a-z0-9-]+\.md' | sort -u)
done

[[ $count -gt 0 ]] || err "no ADR files found in $adr_dir"

if [[ $fail -ne 0 ]]; then
  printf 'adr-lint: FAILED\n' >&2
  exit 1
fi
printf 'adr-lint: OK (%d ADRs)\n' "$count"
