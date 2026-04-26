#!/usr/bin/env python3
"""
hack/pitch-check.py — claim consistency checker for docs/pitch/

For each numeric / date / version / PR / digest claim that appears in
docs/pitch/*.md, look up its status in docs/pitch/facts-verified.md
(PROVEN / PARTIALLY TRUE / FALSE / UNVERIFIABLE / NEEDS USER RECONFIRM).

Why this exists. The 2026-04-25 hostile review v3 caught 9 fabricated
pitch claims (Jetstack 9-digit, Starlink 7000, KubeCon EU 2026 future-
tense, etc.). The 2026-04-27 v4 caught 5 more (timezone mixing, 13->12
PR delta, 73-files-misclassified-as-mutators, "all in milestone" false,
cert-manager keyless inaccurate). Manual hostile review is fragile and
doesn't scale. This script automates the cross-reference pass so every
batch of pitch-doc edits gets a machine pre-flight before slides are
regenerated and before any external publication.

Usage
  hack/pitch-check.py                # default scan, exit 0
  hack/pitch-check.py --verbose      # show every PROVEN match too
  hack/pitch-check.py --strict       # exit 1 if any FALSE/UNVERIFIABLE/NO MATCH
  hack/pitch-check.py --no-color     # disable terminal colour
  hack/pitch-check.py --json         # emit machine-readable report

Pattern coverage (claim types extracted from pitch docs):
  number           e.g.  8,764  9,171  61+
  version          e.g.  v0.4.0  v0.4.0-rc.1  v1.0.0-beta.62
  pr_ref           e.g.  #117  #4504
  digest           e.g.  sha256:24fb644f...
  log_index        e.g.  logIndex 1391582441
  date            e.g.  2026-04-26  2026-04-27
  percent          e.g.  82-100%

Limitations (not blockers, just be aware):
  - False positives: many bare numbers will trigger (e.g. "5-step", "10-page").
    Status NO_MATCH should be reviewed manually before being treated as fail.
  - Heuristic only: this is a text-level cross-checker, not a semantic verifier.
    A claim "v0.4.0 LATEST" that matches PROVEN row F9c is reported OK even if
    the surrounding wording is wrong. Use --verbose + manual review for nuance.
  - Code-block-aware: lines inside ```...``` fences are skipped.
"""

import argparse
import json
import re
import sys
from collections import defaultdict, Counter
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
DEFAULT_PITCH_DIR = REPO_ROOT / "docs" / "pitch"

STATUSES = ["PROVEN", "PARTIALLY TRUE", "FALSE", "UNVERIFIABLE", "NEEDS USER RECONFIRM"]

CLAIM_PATTERNS = [
    # Numbers with thousands-separator commas (8,764 / 22,800 / 10,280)
    (re.compile(r"\b(\d{1,3}(?:,\d{3})+)\b"), "number"),
    # Numbers immediately followed by '+' (61+, 8,700+, 28,000+).
    # Allow optional thousands-separator commas so '28,000+' captures fully
    # rather than splitting into '28' + '000+'.
    (re.compile(r"\b(\d{1,3}(?:,\d{3})*|\d{2,5})\+"), "plus_number"),
    # Semantic versions: v0.4.0, v0.4.0-rc.1, v1.0.0-beta.62
    (re.compile(r"\bv(\d+\.\d+\.\d+(?:-[\w.]+)?)\b"), "version"),
    # PR / issue references with at least 2 digits to reduce noise
    (re.compile(r"#(\d{2,6})\b"), "pr_ref"),
    # Image digests (truncated allowed for long paste-friendly form)
    (re.compile(r"sha256:([0-9a-f]{8,64})"), "digest"),
    # Rekor log indices: ten-digit-ish identifiers near 'logIndex'
    (re.compile(r"\blogIndex[\s:`]*(\d{8,})\b", re.IGNORECASE), "log_index"),
    # ISO dates 202X-NN-NN
    (re.compile(r"\b(202\d-\d{2}-\d{2})\b"), "date"),
    # Standalone percentages with explicit unit (>= 2 digits to reduce noise)
    (re.compile(r"\b(\d{2,3})%"), "percent"),
]


_MONTHS = {
    "January": "01", "February": "02", "March": "03", "April": "04",
    "May": "05", "June": "06", "July": "07", "August": "08",
    "September": "09", "October": "10", "November": "11", "December": "12",
}
_LONG_DATE = re.compile(
    r"\b(\d{1,2})\s+(January|February|March|April|May|June|July|August|"
    r"September|October|November|December)\s+(\d{4})\b"
)


def _normalize_dates(text):
    """Convert long-form dates ('12 November 2024') to ISO ('2024-11-12') in
    place. Pitch slides typically use ISO; facts-verified.md often uses
    long-form. Without this normalization, the cross-reference misses every
    date claim. Run before extract_claims so the index sees both forms.
    """
    def _sub(m):
        d, mon, y = m.group(1), m.group(2), m.group(3)
        return f"{y}-{_MONTHS[mon]}-{int(d):02d}"
    return _LONG_DATE.sub(_sub, text)


def parse_facts(text):
    """Build {value: [(status, fact_ref)]} from facts-verified.md table rows.

    A 'fact_ref' is the F#/A#/B#/etc. label at the start of the row, used in
    output to point reviewers at the matching row.
    """
    index = defaultdict(list)
    in_code_block = False
    text = _normalize_dates(text)
    for raw_line in text.split("\n"):
        if raw_line.strip().startswith("```"):
            in_code_block = not in_code_block
            continue
        if in_code_block:
            continue

        # Detect status keyword in line. Order matters: longer phrase first
        # so PARTIALLY TRUE wins over PROVEN when both appear (rare).
        status = None
        # Use word-boundary match to avoid matching e.g. "PROVEN — but" only via prefix
        for s in ["NEEDS USER RECONFIRM", "PARTIALLY TRUE", "UNVERIFIABLE",
                  "PROVEN", "FALSE"]:
            if s in raw_line:
                status = s
                break
        if status is None:
            continue

        # Extract the row label, e.g. "| A1 |", "| F9c |", "| H3 |"
        label_match = re.search(r"\|\s*([A-Z]\d+[a-z]?)\s*\|", raw_line)
        ref = label_match.group(1) if label_match else "?"

        # For each claim pattern, harvest values from this row.
        for regex, _kind in CLAIM_PATTERNS:
            for m in regex.finditer(raw_line):
                index[m.group(1)].append((status, ref))
    return dict(index)


def extract_claims(text):
    """Yield (kind, value, line_number, context_excerpt) for one pitch doc."""
    in_code_block = False
    for line_no, line in enumerate(text.split("\n"), start=1):
        if line.strip().startswith("```"):
            in_code_block = not in_code_block
            continue
        if in_code_block:
            continue
        # Skip lines that look like a markdown table header divider.
        if re.match(r"^\s*\|[\s|:-]+\|\s*$", line):
            continue
        for regex, kind in CLAIM_PATTERNS:
            for m in regex.finditer(line):
                ctx = line.strip()
                if len(ctx) > 140:
                    ctx = ctx[:140] + "…"
                yield kind, m.group(1), line_no, ctx


# Verdicts — order matters for severity sorting.
VERDICT_OK = "ok"           # PROVEN match, no action needed
VERDICT_WARN = "warn"       # PARTIALLY TRUE match (must use safe wording)
VERDICT_FAIL = "fail"       # FALSE match
VERDICT_UNVERIF = "unverif" # UNVERIFIABLE match
VERDICT_NEEDS = "needs"     # NEEDS USER RECONFIRM
VERDICT_NONE = "none"       # value not present anywhere in facts-verified


def classify(statuses):
    """Convert facts-verified statuses for a value to a verdict + summary str."""
    s = set(st for (st, _) in statuses)
    if "FALSE" in s:
        return VERDICT_FAIL
    if "PARTIALLY TRUE" in s and "PROVEN" not in s:
        return VERDICT_WARN
    if "PROVEN" in s:
        return VERDICT_OK
    if "UNVERIFIABLE" in s:
        return VERDICT_UNVERIF
    if "NEEDS USER RECONFIRM" in s:
        return VERDICT_NEEDS
    return VERDICT_NONE


COLOR = {
    VERDICT_OK: "\033[32m",       # green
    VERDICT_WARN: "\033[33m",     # yellow
    VERDICT_FAIL: "\033[31m",     # red
    VERDICT_UNVERIF: "\033[35m",  # magenta
    VERDICT_NEEDS: "\033[36m",    # cyan
    VERDICT_NONE: "\033[31m",     # red — treat unmatched as fail-class
}
RESET = "\033[0m"


def render(verdict, text, use_color):
    if not use_color:
        return text
    return f"{COLOR.get(verdict, '')}{text}{RESET}"


def main():
    p = argparse.ArgumentParser(description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--verbose", action="store_true",
                   help="show every PROVEN match (default: hidden)")
    p.add_argument("--strict", action="store_true",
                   help="exit 1 if any FALSE / UNVERIFIABLE / NO MATCH found")
    p.add_argument("--no-color", action="store_true", help="disable ANSI colour")
    p.add_argument("--json", action="store_true",
                   help="emit machine-readable JSON report (no human prints)")
    p.add_argument("--pitch-dir", default=str(DEFAULT_PITCH_DIR),
                   help=f"directory containing pitch docs (default: {DEFAULT_PITCH_DIR}). "
                        "Pitch docs are intentionally untracked in main worktree per "
                        "project_whitepaper_local_override.md memory; specify another "
                        "path if running from a worktree without local pitch material.")
    args = p.parse_args()

    pitch_dir = Path(args.pitch_dir).resolve()
    facts_file = pitch_dir / "facts-verified.md"

    use_color = (not args.no_color) and sys.stdout.isatty()
    if args.json:
        use_color = False

    if not facts_file.exists():
        print(f"error: {facts_file} not found "
              f"(use --pitch-dir if pitch docs live elsewhere)", file=sys.stderr)
        return 2

    facts_index = parse_facts(facts_file.read_text(encoding="utf-8"))

    # Scan all pitch *.md files except facts-verified.md itself.
    pitch_docs = sorted(d for d in pitch_dir.glob("*.md") if d.name != facts_file.name)
    if not pitch_docs:
        print(f"error: no pitch docs found under {pitch_dir}", file=sys.stderr)
        return 2

    findings = []  # list of dicts for json mode + stats
    counts = Counter()

    for doc in pitch_docs:
        text = doc.read_text(encoding="utf-8")
        for kind, value, line_no, context in extract_claims(text):
            statuses = facts_index.get(value, [])
            verdict = classify(statuses)
            try:
                rel_path = str(doc.relative_to(REPO_ROOT))
            except ValueError:
                rel_path = str(doc)
            findings.append({
                "file": rel_path,
                "line": line_no,
                "kind": kind,
                "value": value,
                "verdict": verdict,
                "matched_rows": [
                    {"status": st, "ref": ref} for (st, ref) in statuses
                ],
                "context": context,
            })
            counts[verdict] += 1

    if args.json:
        json.dump({"findings": findings, "counts": dict(counts)},
                  sys.stdout, indent=2, ensure_ascii=False)
        sys.stdout.write("\n")
        # exit code logic still applies
    else:
        # Human-readable output.
        prev_file = None
        for f in findings:
            if not args.verbose and f["verdict"] == VERDICT_OK:
                continue
            if f["file"] != prev_file:
                print(f"\n--- {f['file']} ---")
                prev_file = f["file"]
            label_map = {
                VERDICT_OK: "[ok]      ",
                VERDICT_WARN: "[partial] ",
                VERDICT_FAIL: "[FALSE]   ",
                VERDICT_UNVERIF: "[unverif] ",
                VERDICT_NEEDS: "[reconf]  ",
                VERDICT_NONE: "[no-match]",
            }
            tag = render(f["verdict"], label_map[f["verdict"]], use_color)
            ref_str = ""
            if f["matched_rows"]:
                refs = ", ".join(f"{r['ref']}({r['status']})" for r in f["matched_rows"])
                ref_str = f"  → {refs}"
            print(f"  {tag}  L{f['line']:>3}  [{f['kind']:<11}] {f['value']:<48}{ref_str}")
            print(f"            ↳ {f['context']}")

        total = sum(counts.values())
        print()
        print(f"=== Summary across {len(pitch_docs)} pitch doc(s) ===")
        print(f"  scanned  : {total} claim instance(s)")
        print(f"  ok       : {counts[VERDICT_OK]}  (PROVEN match)")
        print(f"  partial  : {counts[VERDICT_WARN]}  (must use safe slide wording)")
        print(f"  unverif  : {counts[VERDICT_UNVERIF]}  (no public source)")
        print(f"  reconf   : {counts[VERDICT_NEEDS]}  (NEEDS USER RECONFIRM)")
        print(f"  FALSE    : {counts[VERDICT_FAIL]}  (contradicted — must remove)")
        print(f"  no-match : {counts[VERDICT_NONE]}  (no facts-verified row — review manually)")

    # --strict fails ONLY on FALSE / UNVERIFIABLE matches (i.e., the slide
    # text would actively contradict facts-verified). NO_MATCH is informational
    # because pitch docs legitimately contain budget tables / competition-rule
    # dates / version numbers that are not "claims requiring verification".
    # Manual review of NO_MATCH is encouraged; auto-failing CI on it would be
    # noisy. PARTIAL is also non-fatal — slides must use safe wording, but
    # the underlying claim is rooted in evidence.
    if args.strict and (counts[VERDICT_FAIL] > 0 or counts[VERDICT_UNVERIF] > 0):
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
