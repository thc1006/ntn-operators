#!/usr/bin/env python3
"""Verify a rendered NTN-operators chart holds the active-passive HA topology (#230).

Usage: verify_ha_topology.py <rendered-manifests.yaml>

Asserts, on a `helm template` render, the structural HA invariants the design
(docs/high-availability.md, #226 / ADR-0005) relies on. A drift that dropped the
soft anti-affinity, changed the default replica count, misaligned the PDB selector
(so minAvailable silently protects nothing), or picked a rollout strategy that can
tear down the healthy version would otherwise ship green.

Exit 0 on pass, 1 on failure (printing every error). Kept as a standalone,
importable module (not inline in the workflow) so the checker itself can be
self-tested against deliberately-broken renders in CI — see
verify_ha_topology_selftest.py.
"""
import math
import sys

import yaml


def _selector_selects(sel, pod_labels):
    """True iff a LabelSelector selects a pod carrying pod_labels, using kube's AND semantics.

    A LabelSelector matches only when EVERY matchLabels entry AND EVERY matchExpressions term holds — so a
    non-matching matchExpressions added alongside a matching matchLabels makes the selector select NOTHING.
    (An OR reading would wrongly pass that case.) An empty selector (no matchLabels, no matchExpressions)
    matches everything in kube, but for our invariants it is NOT an intentional targeting of the manager
    pods, so we treat it as selecting nothing."""
    ml = sel.get('matchLabels') or {}
    exprs = sel.get('matchExpressions') or []
    if not ml and not exprs:
        return False
    for k, v in ml.items():
        if pod_labels.get(k) != v:
            return False
    for expr in exprs:
        key, op, vals = expr.get('key'), expr.get('operator'), expr.get('values') or []
        present = key in pod_labels
        pv = pod_labels.get(key)
        # FAIL-CLOSED: only the four kube operators are valid, with kube's own value rules (In/NotIn require a
        # non-empty values list; Exists/DoesNotExist require an empty one). Anything else is malformed — treat
        # it as NOT selecting the manager rather than silently falling through to True (which would let a
        # bogus selector pass the "selects the manager" gate).
        if op == 'In':
            if not vals or not present or pv not in vals:
                return False
        elif op == 'NotIn':
            if not vals:
                return False
            if present and pv in vals:
                return False
        elif op == 'Exists':
            if vals or not present:
                return False
        elif op == 'DoesNotExist':
            if vals or present:
                return False
        else:
            return False  # unknown operator — malformed, do not treat as a match
    return True


def _as_int(v, default=0):
    """Return v only if it is a REAL int (kube's type for fields like weight); otherwise default. A quoted
    string like "100" is an invalid render (kube wants an int32) and must fail CLOSED — treated as the default
    (so the term is judged ineffective) rather than parsed as 100 or crashing the checker. bool is excluded
    (Python bools are ints)."""
    return v if isinstance(v, int) and not isinstance(v, bool) else default


def _percent(v):
    """Parse an integer-percentage string ("50%" -> 50); return None for a non-integer or non-"%" string
    (e.g. "50.5%", "abc%") so the caller fails closed instead of raising a ValueError traceback."""
    if not isinstance(v, str) or not v.endswith('%'):
        return None
    try:
        return int(v[:-1])
    except ValueError:
        return None


def _pdb_guaranteed_available(pdb_spec, replicas=2):
    """The minimum pods the PDB guarantees stay available, whether written as minAvailable or maxUnavailable,
    int or percentage — so `maxUnavailable: 1` (equivalent to `minAvailable: 1` over 2 replicas) is accepted,
    not falsely rejected. Percentages use kube's WORST-CASE rounding (minAvailable rounds up; maxUnavailable's
    unavailable-count rounds up, i.e. the fewest guaranteed available). Returns None if neither is set or a
    value is a non-percentage string (kube itself rejects those; the caller flags them)."""
    mn, mx = pdb_spec.get('minAvailable'), pdb_spec.get('maxUnavailable')
    if mn is not None and mx is not None:
        return None  # kube forbids setting BOTH — treat as unresolvable so the caller flags it
    if mn is not None:
        if isinstance(mn, bool):
            return None
        if isinstance(mn, str):
            pct = _percent(mn)
            return math.ceil(pct / 100 * replicas) if pct is not None else None
        return int(mn)
    if mx is not None:
        if isinstance(mx, bool):
            return None
        if isinstance(mx, str):
            pct = _percent(mx)
            return replicas - math.ceil(pct / 100 * replicas) if pct is not None else None
        return replicas - int(mx)
    return None


def _effective_max_unavailable(strategy, replicas=2):
    """Kube's effective maxUnavailable over `replicas` (percentages round DOWN; absent => default 25%)."""
    mu = (strategy.get('rollingUpdate') or {}).get('maxUnavailable')
    if mu is None:
        return 0  # kube default 25% of 2 replicas -> floor(0.5) = 0
    if isinstance(mu, str) and mu.endswith('%'):
        return math.floor(int(mu[:-1]) / 100 * replicas)
    return int(mu)


def _effective_max_surge(strategy, replicas=2):
    """Kube's effective maxSurge over `replicas` (percentages round UP; absent => default 25%)."""
    ms = (strategy.get('rollingUpdate') or {}).get('maxSurge')
    if ms is None:
        return 1  # kube default 25% of 2 replicas -> ceil(0.5) = 1
    if isinstance(ms, str) and ms.endswith('%'):
        return math.ceil(int(ms[:-1]) / 100 * replicas)
    return int(ms)


# The manager's distinguishing label — the SAME identity the E2E's managerSelector uses. A PDB or
# anti-affinity selector that PINS this (matchLabels control-plane=controller-manager) cannot also select
# non-manager pods (nothing else in the namespace carries it). One that OMITS it and leans on a shared app
# label (e.g. app.kubernetes.io/name) is over-broad: it still matches the manager, so a plain "does it select
# the manager" check passes, yet it would protect/spread the WRONG set. A namespace holding only manager pods
# cannot expose this at runtime (the count is still right), so it must be caught structurally here.
MANAGER_LABEL_KEY = 'control-plane'
MANAGER_LABEL_VAL = 'controller-manager'


def _pins_manager_identity(sel):
    """True iff the selector fixes the manager's distinguishing label via matchLabels (so it cannot be over-broad)."""
    return (sel.get('matchLabels') or {}).get(MANAGER_LABEL_KEY) == MANAGER_LABEL_VAL


# Anti-affinity term fields that widen the scope beyond "same-namespace manager pods". A drift adding any of
# these makes the spread stop targeting the manager replicas even though the labelSelector still looks right
# (e.g. namespaces:[other] or namespaceSelector:{} score against a different pod set). _selector_selects models
# only the labelSelector, so any field that changes the pod SET must be rejected fail-closed here.
_SCOPE_WIDENING_TERM_FIELDS = ('namespaces', 'namespaceSelector', 'matchLabelKeys', 'mismatchLabelKeys')


def _term_same_namespace_scoped(term):
    """True iff the affinity term stays scoped to the pod's own namespace via labelSelector only — no
    namespaces / namespaceSelector / matchLabelKeys / mismatchLabelKeys that would change the pod set. Note
    namespaceSelector:{} means ALL namespaces (not 'this one'), so any present value is rejected."""
    return all(term.get(f) in (None, []) for f in _SCOPE_WIDENING_TERM_FIELDS)


def check(docs):
    """Return a list of invariant-violation strings for the rendered docs (empty list = all invariants hold)."""
    errs = []
    deps = [d for d in docs if d.get('kind') == 'Deployment']
    pdbs = [d for d in docs if d.get('kind') == 'PodDisruptionBudget']
    if len(deps) != 1:
        errs.append(f"expected exactly 1 Deployment, got {len(deps)}")
    if len(pdbs) != 1:
        errs.append(f"expected exactly 1 PodDisruptionBudget, got {len(pdbs)}")
    if errs:
        return errs
    dep, pdb = deps[0], pdbs[0]
    pod_labels = dep['spec']['template']['metadata'].get('labels') or {}

    # (0) anchor: the pod template must carry the manager's distinguishing identity label. The PDB /
    #     anti-affinity specificity checks below are anchored to it, so a drift that removed it must fail here.
    if pod_labels.get(MANAGER_LABEL_KEY) != MANAGER_LABEL_VAL:
        errs.append(f"pod template must carry the manager identity label {MANAGER_LABEL_KEY}={MANAGER_LABEL_VAL} "
                    f"(got labels {pod_labels})")

    # (1) active-passive default = 2 replicas (one active reconciler + one warm standby). repr() so a string
    #     "2" (invalid; kube wants an int) is visibly distinct from the int 2 in the error.
    if dep['spec'].get('replicas') != 2:
        errs.append(f"default replicas must be 2 (active-passive), got {dep['spec'].get('replicas')!r}")

    # (2) the PDB must guarantee EXACTLY 1 available pod over 2 replicas — a drain may take at most one at a
    #     time. Accept either expression (minAvailable OR maxUnavailable, int OR percentage): `maxUnavailable:
    #     1` is equivalent to `minAvailable: 1` and must not be falsely rejected. A non-percentage string is
    #     invalid (kube rejects it) -> guaranteed is None -> flagged here too.
    guaranteed = _pdb_guaranteed_available(pdb['spec'])
    if guaranteed != 1:
        errs.append(f"PDB must guarantee exactly 1 available pod over 2 replicas (minAvailable/maxUnavailable "
                    f"resolves to {guaranteed!r}); minAvailable={pdb['spec'].get('minAvailable')!r} "
                    f"maxUnavailable={pdb['spec'].get('maxUnavailable')!r}")

    # (3) soft (preferred) pod anti-affinity spreads the standby onto another node; it must NOT be hard
    #     (required), which would wedge scheduling on a single-node cluster. Robust against drift: iterate
    #     EVERY preferred term (not just the first) and require at least one that is actually EFFECTIVE —
    #     positive weight, hostname topology, and a selector (matchLabels or matchExpressions) that selects
    #     the manager pods — else the spread silently guards nothing.
    paa = ((dep['spec']['template']['spec'].get('affinity') or {}).get('podAntiAffinity')) or {}
    if 'requiredDuringSchedulingIgnoredDuringExecution' in paa:
        errs.append("podAntiAffinity must NOT be hard (required) — it would wedge single-node scheduling")
    pref = paa.get('preferredDuringSchedulingIgnoredDuringExecution') or []
    effective = [
        p for p in pref
        if _as_int(p.get('weight')) > 0
        and (p.get('podAffinityTerm') or {}).get('topologyKey') == 'kubernetes.io/hostname'
        and _selector_selects((p.get('podAffinityTerm') or {}).get('labelSelector') or {}, pod_labels)
        and _pins_manager_identity((p.get('podAffinityTerm') or {}).get('labelSelector') or {})
        and _term_same_namespace_scoped(p.get('podAffinityTerm') or {})
    ]
    if not effective:
        errs.append("no EFFECTIVE, manager-specific, same-namespace soft podAntiAffinity term (need weight>0, "
                    f"topologyKey=kubernetes.io/hostname, a selector pinning {MANAGER_LABEL_KEY}="
                    f"{MANAGER_LABEL_VAL} so it spreads ONLY the manager, and NO namespaces/namespaceSelector/"
                    f"matchLabelKeys/mismatchLabelKeys that would re-scope it); saw {len(pref)} preferred term(s)")

    # (4) the PDB must protect EXACTLY the manager workload — its selector must equal the Deployment's OWN
    #     selector (the canonical "match the owning workload" rule). This is strictly stronger than "does it
    #     select a manager pod": it rejects an OVER-broad selector (which would let minAvailable be satisfied
    #     by unrelated pods so both managers could be evicted) AND an under-broad one, neither of which a
    #     manager-only Kind namespace can expose at runtime (the pod count is right regardless). It also
    #     subsumes the manager-identity pin, since the Deployment selector carries it. As a sanity, the
    #     selector must still actually select the pod template (guards a Deployment/PDB rendered inconsistently).
    dep_sel = dep['spec'].get('selector') or {}
    pdb_sel = pdb['spec'].get('selector') or {}
    if pdb_sel != dep_sel:
        errs.append(f"PDB selector must EQUAL the Deployment selector (protect exactly the manager workload); "
                    f"PDB={pdb_sel} Deployment={dep_sel}")
    if not _selector_selects(pdb_sel, pod_labels) or not _pins_manager_identity(pdb_sel):
        errs.append(f"PDB selector does not specifically select the manager pods (selector={pdb_sel}, "
                    f"pod labels={pod_labels})")

    # (5) the cache-sync /readyz rollout gate (TestHACacheSyncRolloutGate) relies on the effective
    #     maxUnavailable being 0 for the default 2 replicas: a broken new pod must NOT tear down the last
    #     healthy one. Assert nothing overrode the default into a >0 value and the strategy is not Recreate.
    strat = dep['spec'].get('strategy') or {}
    if strat.get('type') == 'Recreate':
        errs.append("Deployment strategy must not be Recreate — it tears down the healthy version during a rollout")
    eff_mu = _effective_max_unavailable(strat)
    if eff_mu != 0:
        errs.append(f"effective maxUnavailable over 2 replicas must be 0 (got {eff_mu}); a >0 value lets a "
                    "broken new pod evict the last healthy replica")
    # (6) the same rollout gate needs a surge pod to exist: with maxUnavailable=0, a maxSurge of 0 deadlocks
    #     the rollout (kube can neither add nor remove a pod), so the cache-sync test could never observe a
    #     gated new pod. Require effective maxSurge >= 1.
    eff_ms = _effective_max_surge(strat)
    if eff_ms < 1:
        errs.append(f"effective maxSurge over 2 replicas must be >= 1 (got {eff_ms}); with maxUnavailable=0 a "
                    "0 surge deadlocks the rollout — the cache-sync gate test could never see a new pod")

    # (7) co-location guard: the soft anti-affinity above exists to SPREAD the two replicas across hosts, but
    #     several OTHER pod-spec fields can silently pin BOTH onto one node — defeating HA entirely while the
    #     anti-affinity still "looks right". Reject: a fixed spec.nodeName; a nodeSelector on the hostname
    #     label; a nodeAffinity (required or preferred) that references the hostname label or pins the node
    #     name; and a podAffinity (ATTRACTION, hard or soft) on the hostname topology selecting the manager
    #     (which pulls the replicas together — the opposite of anti-affinity).
    # NOTE: this guard covers the kubernetes.io/hostname pinning dimension only — a pin via a custom single-node
    # label (a rack/zone label that happens to map to one node) cannot be detected from a render alone.
    podspec = dep['spec']['template']['spec']
    if podspec.get('nodeName'):
        errs.append(f"pod template sets spec.nodeName={podspec.get('nodeName')!r} — pins ALL replicas to one "
                    "node, defeating the HA spread")
    if 'kubernetes.io/hostname' in (podspec.get('nodeSelector') or {}):
        errs.append("pod template nodeSelector pins kubernetes.io/hostname — forces all replicas onto one node")
    affinity = podspec.get('affinity') or {}
    na = affinity.get('nodeAffinity') or {}
    na_terms = list((na.get('requiredDuringSchedulingIgnoredDuringExecution') or {}).get('nodeSelectorTerms') or [])
    na_terms += [(p.get('preference') or {}) for p in (na.get('preferredDuringSchedulingIgnoredDuringExecution') or [])]
    for term in na_terms:
        # Only a POSITIVE pin co-locates: hostname/metadata.name `In` WITH values. NotIn/Exists/DoesNotExist are
        # exclusions or presence checks that do NOT force all replicas onto one node (so must not be rejected).
        pins = any(e.get('key') == 'kubernetes.io/hostname' and e.get('operator') == 'In' and (e.get('values') or [])
                   for e in (term.get('matchExpressions') or []))
        pins = pins or any(e.get('key') == 'metadata.name' and e.get('operator') == 'In' and (e.get('values') or [])
                           for e in (term.get('matchFields') or []))
        if pins:
            errs.append("nodeAffinity pins the node (kubernetes.io/hostname In / metadata.name In) — can pin all "
                        "replicas to one node, defeating the HA spread")
            break
    pa = affinity.get('podAffinity') or {}
    pa_terms = list(pa.get('requiredDuringSchedulingIgnoredDuringExecution') or [])
    pa_terms += [(p.get('podAffinityTerm') or {}) for p in (pa.get('preferredDuringSchedulingIgnoredDuringExecution') or [])]
    for term in pa_terms:
        if term.get('topologyKey') != 'kubernetes.io/hostname':
            continue
        sel = term.get('labelSelector')
        # An EMPTY {} selector is labels.Everything() (selects ALL pods) -> co-locates; a NIL/absent selector is
        # labels.Nothing() (selects none) -> harmless. matchLabelKeys merges the selector to same-ReplicaSet
        # pods -> co-locates. A non-empty selector co-locates iff it selects the manager.
        if sel == {} or term.get('matchLabelKeys') or (sel and _selector_selects(sel, pod_labels)):
            errs.append("podAffinity ATTRACTS the manager on kubernetes.io/hostname — pulls both replicas onto "
                        "one node, the opposite of the anti-affinity spread")
            break

    # (8) a paused Deployment ignores all spec updates, so the cache-sync rollout gate (checks 5/6) can never
    #     fire — the manager would never actually roll. Reject spec.paused.
    if dep['spec'].get('paused'):
        errs.append("Deployment spec.paused is true — it would never roll out, so the rollout-gate invariants "
                    "cannot hold")
    return errs


def main(argv):
    if len(argv) != 2:
        print("usage: verify_ha_topology.py <rendered-manifests.yaml>", file=sys.stderr)
        return 2
    with open(argv[1]) as f:
        docs = [d for d in yaml.safe_load_all(f) if d]
    errs = check(docs)
    if errs:
        print("FAIL: chart HA topology:\n  - " + "\n  - ".join(errs))
        return 1
    print("chart HA topology OK (replicas=2, PDB minAvailable=1 selecting the manager pods, "
          "effective maxUnavailable=0, effective soft anti-affinity on kubernetes.io/hostname)")
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv))
