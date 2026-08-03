#!/usr/bin/env bash
# Validate docs/adr/ integrity so a numbering collision (two ADR-0010s) or a
# dangling cross-reference cannot merge. Checks, per ADR file:
#   - the 4-digit number is unique across docs/adr/
#   - the "# ADR NNNN" title matches the filename number
#   - a "- Status:" line is present
#   - the ADR is listed in the index (docs/adr/README.md)
#   - every issue the ADR's own front matter tracks also appears in its index row
#   - every relative link to another ADR file resolves
#   - every internal #anchor link resolves to a heading in the target file
#     (repo-internal only; external URLs are never fetched, so a site being
#     temporarily down can never make this lint flaky)
set -euo pipefail

adr_dir="docs/adr"
readme="$adr_dir/README.md"
fail=0
err() { printf 'adr-lint: %s\n' "$*" >&2; fail=1; }

# slugify turns a heading (stdin, already stripped of leading #'s) into the
# GitHub-flavored anchor slug: lowercase, drop everything but [a-z0-9 _-], spaces
# to hyphens, trim edge hyphens. Consecutive spaces stay as consecutive hyphens
# (so "a  b" -> "a--b"), matching GitHub.
slugify() {
  tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9 _-]//g; s/ /-/g; s/^-+//; s/-+$//'
}

# file_anchors prints every heading's slug for a file, one per line.
file_anchors() {
  grep -E '^#{1,6}[[:space:]]+' "$1" | sed -E 's/^#{1,6}[[:space:]]+//' \
    | while IFS= read -r h; do printf '%s\n' "$h" | slugify; done
}

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

  grep -qE '^status:|^- Status:' "$f" || err "$base: missing status (front-matter 'status:' or '- Status:' line)"

  grep -qE "(^|[^0-9])$num([^0-9]|\$)" "$readme" || err "$base: ADR $num not listed in $readme"

  # Tracking drift. The index row may say MORE than the front matter — it carries
  # implementation pointers ("impl #256") the ADR itself has no reason to list — but it
  # must not say LESS, or the one place a reader is told to start goes stale while every
  # other check still passes. That is not hypothetical: the index kept pointing at a
  # closed duplicate for ADR-0010 while the ADR tracked a different issue, and an ADR-0008
  # edit replaced a tracked follow-up with prose. Containment, not equality, is what makes
  # this checkable without forbidding the editorial extras that make the index useful.
  row=$(grep -m1 -E "^\| \[?$num[](]" "$readme" || true)
  if [[ -n "$row" ]]; then
    # || true throughout: a row or an ADR with no #refs is legitimate, but an empty grep
    # exits 1 and, under `set -e -o pipefail`, would abort the whole lint with no message.
    row_refs=$(printf '%s' "$row" | { grep -oE '#[0-9]+' || true; } | tr -d '#' | sort -u)
    while IFS= read -r tracked; do
      [[ -z "$tracked" ]] && continue
      grep -qxF -- "$tracked" <<<"$row_refs" \
        || err "$base: front matter tracks #$tracked but the index row does not mention it"
    done < <(sed -n '/^tracking:/,/^[a-z_-]*:/p' "$f" \
               | { grep -oE '(issues|pull)/[0-9]+' || true; } | { grep -oE '[0-9]+' || true; } | sort -u)
  fi

  while IFS= read -r target; do
    [[ -z "$target" ]] && continue
    [[ -f "$adr_dir/$target" ]] || err "$base: broken relative link -> $target"
  done < <(grep -oE '\]\(\.?/?[0-9]{4}-[a-z0-9-]+\.md' "$f" | grep -oE '[0-9]{4}-[a-z0-9-]+\.md' | sort -u)

  # Internal #anchor links: same-file (#a), another ADR (NNNN-*.md#a), or the
  # index (README.md#a). Each must resolve to a heading in the target file.
  # http(s) links are excluded by the pattern, so this never touches the network.
  while IFS= read -r link; do
    [[ -z "$link" ]] && continue
    tgt=${link%%#*}
    anchor=$(printf '%s' "${link#*#}" | tr '[:upper:]' '[:lower:]')
    tgtfile=$([[ -z "$tgt" ]] && printf '%s' "$f" || printf '%s' "$adr_dir/$tgt")
    if [[ ! -f "$tgtfile" ]]; then
      err "$base: anchor link to missing file -> $tgt"
    elif ! grep -qxF -- "$anchor" < <(file_anchors "$tgtfile"); then
      # process substitution, not a pipe: grep -q exiting early on a match would
      # SIGPIPE file_anchors and, under pipefail, make the pipeline report failure
      # despite the match. A redirected fd has no such interaction.
      err "$base: broken anchor -> ${tgt}#$anchor"
    fi
  done < <(grep -oE '\]\((#[A-Za-z0-9._-]+|(\.?/?[0-9]{4}-[a-z0-9-]+\.md|README\.md)#[A-Za-z0-9._-]+)\)' "$f" \
             | sed -E 's/^\]\(//; s/\)$//; s#^\./##')
done

[[ $count -gt 0 ]] || err "no ADR files found in $adr_dir"

if [[ $fail -ne 0 ]]; then
  printf 'adr-lint: FAILED\n' >&2
  exit 1
fi
printf 'adr-lint: OK (%d ADRs)\n' "$count"
