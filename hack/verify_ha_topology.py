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
        if op == 'In' and (not present or pv not in vals):
            return False
        if op == 'NotIn' and (present and pv in vals):
            return False
        if op == 'Exists' and not present:
            return False
        if op == 'DoesNotExist' and present:
            return False
    return True


def _effective_max_unavailable(strategy, replicas=2):
    """Kube's effective maxUnavailable over `replicas` (percentages round DOWN; absent => default 25%)."""
    mu = (strategy.get('rollingUpdate') or {}).get('maxUnavailable')
    if mu is None:
        return 0  # kube default 25% of 2 replicas -> floor(0.5) = 0
    if isinstance(mu, str) and mu.endswith('%'):
        return math.floor(int(mu[:-1]) / 100 * replicas)
    return int(mu)


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

    # (1) active-passive default = 2 replicas (one active reconciler + one warm standby).
    if dep['spec'].get('replicas') != 2:
        errs.append(f"default replicas must be 2 (active-passive), got {dep['spec'].get('replicas')}")

    # (2) PDB minAvailable=1 over 2 replicas -> a drain may take at most one at a time.
    if pdb['spec'].get('minAvailable') != 1:
        errs.append(f"PDB minAvailable must be 1, got {pdb['spec'].get('minAvailable')}")

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
        if (p.get('weight') or 0) > 0
        and (p.get('podAffinityTerm') or {}).get('topologyKey') == 'kubernetes.io/hostname'
        and _selector_selects((p.get('podAffinityTerm') or {}).get('labelSelector') or {}, pod_labels)
    ]
    if not effective:
        errs.append("no EFFECTIVE soft podAntiAffinity term (need weight>0, "
                    f"topologyKey=kubernetes.io/hostname, and a selector selecting the manager pods); "
                    f"saw {len(pref)} preferred term(s)")

    # (4) selector alignment: the PDB must actually SELECT the manager pods, else minAvailable guards nothing.
    #     Uses full LabelSelector (matchLabels AND matchExpressions) semantics, so a non-matching
    #     matchExpressions slipped alongside the matchLabels is caught (it would select no pods).
    pdb_sel = pdb['spec'].get('selector') or {}
    if not _selector_selects(pdb_sel, pod_labels):
        errs.append(f"PDB selector does not select the manager pods (selector={pdb_sel}, pod labels={pod_labels})")

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
