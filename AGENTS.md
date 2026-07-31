# NTN Operators — agent guide

Kubernetes operators for Non-Terrestrial Networks (NTN). 4 CRDs: `SatelliteEphemeris`, `GroundStationLifecycle`, `NTNCellConfig`, `NTNSlice`.

Shared, tool-neutral context for AI coding agents (the `AGENTS.md` convention). Cursor, Codex, Copilot and similar read this file directly. Claude Code reads `CLAUDE.md`, not `AGENTS.md` — to pick this up, keep a local `CLAUDE.md` containing `@AGENTS.md` (or `ln -s AGENTS.md CLAUDE.md`).

## Architecture decisions
`docs/adr/` is the single authoritative source for every architecture decision. **Read `docs/adr/README.md` (the index) at the start of any design or ADR-related work** — it states each decision, its status, and what supersedes it — before proposing or changing a design.
- Front matter + structure follow `docs/adr/ADR_STYLE_GUIDE.md`; `docs/adr/SOURCES.md` is the verification register (normative standard first).
- Validate ADR changes with `make adr-lint` (`checks/check_adr_metadata.py` + `hack/check-adr-index.sh`).
- ADR numbers are unique and machine-checked; `0004` is intentionally reserved/absent.

## Working conventions
- Develop feature work on a git worktree/branch; never commit directly on `main`.
- Run `make lint && make test` before opening a PR; `make adr-lint` for ADR changes.
- Do not add AI-generated commit footers or `Co-authored-by` trailers.
- CRD field/status changes: regenerate and sync all four CRD copies with `make manifests bundle nephio-sync` (config/crd, Helm, OLM bundle, Nephio).
- An `Accepted` ADR is not `Implemented`: record implementation PRs explicitly; never claim unmerged work has shipped.
