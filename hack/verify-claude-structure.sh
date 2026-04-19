#!/usr/bin/env bash
# Verify .claude/ follows the flat Claude Code directory structure.
# Usage: hack/verify-claude-structure.sh [--strict AGENTS COMMANDS SKILLS]
# Example: hack/verify-claude-structure.sh --strict 45 33 32

set -euo pipefail
ERRORS=0
STRICT=""
EXPECT_AGENTS=0; EXPECT_COMMANDS=0; EXPECT_SKILLS=0
if [ "${1:-}" = "--strict" ] && [ $# -eq 4 ]; then
  STRICT=1; EXPECT_AGENTS=$2; EXPECT_COMMANDS=$3; EXPECT_SKILLS=$4
fi

echo "=== Verifying .claude/ flat structure ==="

# 0. Required directories must exist
for dir in .claude/agents .claude/commands .claude/skills; do
  if [ ! -d "$dir" ]; then
    echo "ERROR: required directory $dir does not exist"
    ERRORS=$((ERRORS + 1))
  fi
done

# 1. No nested directories under .claude/agents/ or .claude/commands/
for dir in .claude/agents .claude/commands; do
  while IFS= read -r d; do
    echo "ERROR: nested directory in ${dir}/: $d"
    ERRORS=$((ERRORS + 1))
  done < <(find "$dir" -mindepth 1 -type d 2>/dev/null)
done

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

# 4. Count summary (enforced only with --strict)
agents=$(find .claude/agents -maxdepth 1 -name '*.md' -type f 2>/dev/null | wc -l)
commands=$(find .claude/commands -maxdepth 1 -name '*.md' -type f 2>/dev/null | wc -l)
skills=$(find .claude/skills -maxdepth 1 -mindepth 1 -type d 2>/dev/null | wc -l)
echo ""
echo "Agents:   $agents"
echo "Commands: $commands"
echo "Skills:   $skills"

if [ -n "$STRICT" ]; then
  [ "$agents" -eq "$EXPECT_AGENTS" ] || { echo "ERROR: expected $EXPECT_AGENTS agents, got $agents"; ERRORS=$((ERRORS + 1)); }
  [ "$commands" -eq "$EXPECT_COMMANDS" ] || { echo "ERROR: expected $EXPECT_COMMANDS commands, got $commands"; ERRORS=$((ERRORS + 1)); }
  [ "$skills" -eq "$EXPECT_SKILLS" ] || { echo "ERROR: expected $EXPECT_SKILLS skills, got $skills"; ERRORS=$((ERRORS + 1)); }
fi

if [ "$ERRORS" -eq 0 ]; then
  echo ""
  echo "PASS: .claude/ structure is valid."
else
  echo ""
  echo "FAIL: $ERRORS error(s) found."
  exit 1
fi
