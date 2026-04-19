#!/bin/bash
# Verify .claude/ follows the flat Claude Code directory structure.
# Usage: hack/verify-claude-structure.sh

set -euo pipefail
ERRORS=0

echo "=== Verifying .claude/ flat structure ==="

# 1. No nested directories under .claude/agents/
while IFS= read -r d; do
  echo "ERROR: nested directory in .claude/agents/: $d"
  ERRORS=$((ERRORS + 1))
done < <(find .claude/agents -mindepth 1 -type d 2>/dev/null)

# 2. All agent files have valid frontmatter with matching name
for f in .claude/agents/*.md; do
  [ -f "$f" ] || continue
  if ! head -1 "$f" | grep -q '^---'; then
    echo "ERROR: missing frontmatter in $f"
    ERRORS=$((ERRORS + 1))
    continue
  fi
  name=$(head -10 "$f" | grep '^name:' | head -1 | sed 's/^name: *//')
  expected=$(basename "$f" .md)
  if [ "$name" != "$expected" ]; then
    echo "ERROR: filename/name mismatch in $f: name='$name' expected='$expected'"
    ERRORS=$((ERRORS + 1))
  fi
done

# 3. All skill directories have SKILL.md
for d in .claude/skills/*/; do
  [ -d "$d" ] || continue
  if [ ! -f "${d}SKILL.md" ]; then
    echo "ERROR: missing SKILL.md in $d"
    ERRORS=$((ERRORS + 1))
  fi
done

# 4. Count verification
agents=$(find .claude/agents -maxdepth 1 -name '*.md' -type f 2>/dev/null | wc -l)
commands=$(find .claude/commands -maxdepth 1 -name '*.md' -type f 2>/dev/null | wc -l)
skills=$(find .claude/skills -maxdepth 1 -mindepth 1 -type d 2>/dev/null | wc -l)
echo ""
echo "Agents:   $agents (expected: 45)"
echo "Commands: $commands (expected: 33)"
echo "Skills:   $skills (expected: 32)"

if [ "$ERRORS" -eq 0 ]; then
  echo ""
  echo "PASS: .claude/ structure is valid."
else
  echo ""
  echo "FAIL: $ERRORS error(s) found."
  exit 1
fi
