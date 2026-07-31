# ADR authoring and AI-assisted development guide

This repository treats an ADR as the **single current authority for a decision**,
not as a chronological scratchpad.

## Required metadata

Every ADR must contain YAML front matter with:

- `adr`: unique integer;
- `title`;
- `status`: `proposed`, `accepted`, `deprecated`, or `superseded`;
- `date`;
- `last_verified`;
- `deciders`;
- `supersedes` and `superseded_by`;
- `implementation`;
- `tracking`.

`checks/check_adr_metadata.py` rejects duplicate numbers and malformed status.

## Required content

An ADR should contain, in order:

1. **Decision summary** — the decision in a few sentences.
2. **Context** — repository facts separated from external facts.
3. **Decision drivers** — requirements and constraints.
4. **Decision** — normative MUST/SHOULD/MAY language.
5. **Invariants** — properties that must remain true after refactors.
6. **Alternatives** — rejected or deferred options and why.
7. **Failure modes and security** — what can go wrong and how it fails.
8. **Compatibility and migration** — existing objects, old clients, rollback.
9. **Observability** — status, events, metrics, logs and operator action.
10. **Test plan** — executable acceptance criteria.
11. **Rollout** — small, ordered, reversible changes.
12. **References** — primary sources and exact upstream commits where possible.

## Rules for AI-assisted work

- Never turn an inference into a repository fact. Label it as an assumption.
- Never cite “upstream HEAD” without a commit SHA and verification date.
- Do not use five weak sources to outweigh one normative standard. Prefer the
  standard, official documentation, source code and executable tests.
- Generated code is not accepted because it compiles. It must pass unit,
  envtest, race, integration and negative/security tests appropriate to scope.
- Preserve API semantics during conversion. A conversion webhook is not a
  migration or a place to silently “improve” user intent.
- Avoid hidden defaults for security-sensitive behavior. Require explicit user
  intent and an admin-controlled outer policy.
- A condition must describe evidence the controller actually observed.
  `Unknown` is correct when evidence is absent.
- No “fallback succeeded” wording may imply that a runtime-only action reached
  hardware when only desired state was persisted.
- Security controls must be layered as an intersection, never as substitutes:
  admission authorization ∩ runtime authorization ∩ destination policy ∩
  network enforcement.
- Every security decision needs mutation-style negative tests: remove the check
  and prove that at least one test fails.
- Amendments that reverse a load-bearing decision require a consolidated
  replacement ADR or a full rewrite. Do not leave mutually valid paragraphs.
- Keep design and implementation status separate. `Accepted` does not mean
  `Implemented`; record implementation PRs explicitly.

## Review checklist

- [ ] ADR number is unique.
- [ ] Status matches implementation reality.
- [ ] External facts are current and primary-source backed.
- [ ] Standards terminology is exact.
- [ ] API defaults do not hide security behavior.
- [ ] Old stored objects and old clients have a migration path.
- [ ] Failure states are observable and recoverable.
- [ ] No condition reports `True` without supporting evidence.
- [ ] Tests include happy path, malformed input, unavailable dependency,
      stale data, restart/failover and authorization denial.
- [ ] Rollback has been designed before rollout.
