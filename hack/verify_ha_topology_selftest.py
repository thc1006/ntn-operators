#!/usr/bin/env python3
"""Negative-control self-test for verify_ha_topology.check (#230).

Usage: verify_ha_topology_selftest.py <known-good-render.yaml>

Given the SAME known-good render the positive check runs on, apply each drift we care about and assert the
checker REJECTS every one — and still ACCEPTS the unmutated base. A refactor that silently broke a guard (so
real drift would ship green) fails HERE instead. This is the negative-control layer that was previously only
run by hand; run it in CI right after the positive check.
"""
import copy
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import yaml  # noqa: E402  (after sys.path tweak)

from verify_ha_topology import check  # noqa: E402


def _dep(docs):
    return next(d for d in docs if d.get('kind') == 'Deployment')


def _pdb(docs):
    return next(d for d in docs if d.get('kind') == 'PodDisruptionBudget')


def _pref(docs):
    return _dep(docs)['spec']['template']['spec']['affinity']['podAntiAffinity'][
        'preferredDuringSchedulingIgnoredDuringExecution']


def m_replicas(d):
    _dep(d)['spec']['replicas'] = 1


def m_pdb_min(d):
    _pdb(d)['spec']['minAvailable'] = 2


def m_pdb_selector(d):
    _pdb(d)['spec']['selector']['matchLabels'] = {'app': 'does-not-exist'}


def m_recreate(d):
    _dep(d)['spec']['strategy'] = {'type': 'Recreate'}


def m_maxunavail_int(d):
    _dep(d)['spec']['strategy'] = {'type': 'RollingUpdate', 'rollingUpdate': {'maxUnavailable': 1}}


def m_maxunavail_pct(d):
    _dep(d)['spec']['strategy'] = {'type': 'RollingUpdate', 'rollingUpdate': {'maxUnavailable': '50%'}}


def m_aff_hard(d):
    _dep(d)['spec']['template']['spec']['affinity']['podAntiAffinity'][
        'requiredDuringSchedulingIgnoredDuringExecution'] = [{
            'topologyKey': 'kubernetes.io/hostname',
            'labelSelector': {'matchLabels': {'control-plane': 'controller-manager'}},
        }]


def m_aff_weight0(d):
    for p in _pref(d):
        p['weight'] = 0


def m_aff_selector(d):
    _pref(d)[0]['podAffinityTerm']['labelSelector']['matchLabels'] = {'app': 'does-not-exist'}


def m_aff_topology(d):
    _pref(d)[0]['podAffinityTerm']['topologyKey'] = 'topology.kubernetes.io/zone'


def m_pdb_matchexpr(d):
    # A non-matching matchExpressions ANDed onto a good matchLabels makes the selector select NOTHING —
    # an OR reading of the selector would wrongly accept this.
    _pdb(d)['spec']['selector'].setdefault('matchExpressions', []).append(
        {'key': 'nonexistent', 'operator': 'Exists'})


def m_aff_matchexpr(d):
    _pref(d)[0]['podAffinityTerm']['labelSelector'].setdefault('matchExpressions', []).append(
        {'key': 'nonexistent', 'operator': 'Exists'})


def m_pdb_broad(d):
    # Over-broad: still SELECTS the manager (shared app label) but drops the distinguishing control-plane
    # label, so it could also select non-manager pods. A manager-only namespace cannot expose this at runtime.
    _pdb(d)['spec']['selector']['matchLabels'] = {'app.kubernetes.io/name': 'ntn-operators'}


def m_aff_broad(d):
    _pref(d)[0]['podAffinityTerm']['labelSelector']['matchLabels'] = {'app.kubernetes.io/name': 'ntn-operators'}


def m_maxsurge0(d):
    # With the default maxUnavailable (0 over 2 replicas), a 0 surge deadlocks the rollout.
    _dep(d)['spec']['strategy'] = {'type': 'RollingUpdate', 'rollingUpdate': {'maxSurge': 0}}


def m_pod_identity(d):
    # Remove the manager identity label from the pod template — the anchor the specificity checks rely on.
    _dep(d)['spec']['template']['metadata']['labels'].pop('control-plane', None)


def m_pdb_narrower(d):
    # Narrower than the Deployment selector (adds an extra constraint) — no longer protects exactly the
    # workload. Over-broad is caught by m_pdb_broad; this proves the PDB==Deployment check catches under-broad.
    _pdb(d)['spec']['selector']['matchLabels']['pod-template-hash'] = 'deadbeef'


def m_aff_namespaces(d):
    # Re-scopes the spread to a DIFFERENT namespace — it no longer spreads the manager replicas.
    _pref(d)[0]['podAffinityTerm']['namespaces'] = ['definitely-not-ntn-system']


def m_aff_nsselector(d):
    # namespaceSelector:{} means ALL namespaces, widening the scored pod set beyond the manager's namespace.
    _pref(d)[0]['podAffinityTerm']['namespaceSelector'] = {}


def m_aff_matchlabelkeys(d):
    # matchLabelKeys changes the effective pod set; the checker does not model it, so it must fail closed.
    _pref(d)[0]['podAffinityTerm']['matchLabelKeys'] = ['pod-template-hash']


def m_aff_invalid_op(d):
    # An invalid operator alongside a good matchLabels — the fail-open bug would accept this.
    _pref(d)[0]['podAffinityTerm']['labelSelector'].setdefault('matchExpressions', []).append(
        {'key': 'control-plane', 'operator': 'Superset', 'values': ['x']})


def m_aff_in_empty(d):
    _pref(d)[0]['podAffinityTerm']['labelSelector'].setdefault('matchExpressions', []).append(
        {'key': 'control-plane', 'operator': 'In', 'values': []})


def m_aff_notin_empty(d):
    # NotIn with empty values matches EVERYTHING — over-broad; must be rejected, not treated as a match.
    _pref(d)[0]['podAffinityTerm']['labelSelector'].setdefault('matchExpressions', []).append(
        {'key': 'anything', 'operator': 'NotIn', 'values': []})


# --- co-location (F1): fields that pin BOTH replicas to one node, defeating the anti-affinity spread ---
def m_nodename(d):
    _dep(d)['spec']['template']['spec']['nodeName'] = 'node-a'


def m_nodeselector_host(d):
    _dep(d)['spec']['template']['spec']['nodeSelector'] = {'kubernetes.io/hostname': 'node-a'}


def m_nodeaffinity_host(d):
    _dep(d)['spec']['template']['spec'].setdefault('affinity', {})['nodeAffinity'] = {
        'requiredDuringSchedulingIgnoredDuringExecution': {
            'nodeSelectorTerms': [{'matchExpressions': [
                {'key': 'kubernetes.io/hostname', 'operator': 'In', 'values': ['node-a']}]}],
        },
    }


def m_podaffinity_colocate(d):
    _dep(d)['spec']['template']['spec'].setdefault('affinity', {})['podAffinity'] = {
        'requiredDuringSchedulingIgnoredDuringExecution': [{
            'topologyKey': 'kubernetes.io/hostname',
            'labelSelector': {'matchLabels': {'control-plane': 'controller-manager'}},
        }],
    }


def m_paused(d):
    _dep(d)['spec']['paused'] = True  # a paused Deployment never rolls -> the rollout gate can't fire


def m_weight_str(d):
    for p in _pref(d):
        p['weight'] = '100'  # a string weight must fail closed (no effective term), not crash the checker


def m_pdb_maxunavail_0(d):
    # maxUnavailable:0 guarantees 2 available -> blocks EVERY voluntary disruption (over-strict, wrong).
    _pdb(d)['spec'].pop('minAvailable', None)
    _pdb(d)['spec']['maxUnavailable'] = 0


def m_pdb_maxunavail_2(d):
    # maxUnavailable:2 guarantees 0 available -> the PDB protects nothing.
    _pdb(d)['spec'].pop('minAvailable', None)
    _pdb(d)['spec']['maxUnavailable'] = 2


def m_podaffinity_empty_selector(d):
    # Empty {} labelSelector = labels.Everything() -> selects ALL pods -> co-locates. _selector_selects returns
    # False for {}, so the guard must special-case it (else a silent HA-defeating co-location slips through).
    _dep(d)['spec']['template']['spec'].setdefault('affinity', {})['podAffinity'] = {
        'preferredDuringSchedulingIgnoredDuringExecution': [{
            'weight': 100,
            'podAffinityTerm': {'topologyKey': 'kubernetes.io/hostname', 'labelSelector': {}},
        }],
    }


def m_podaffinity_matchlabelkeys(d):
    # matchLabelKeys merges the selector to same-ReplicaSet pods -> co-locates even with a base {} selector.
    _dep(d)['spec']['template']['spec'].setdefault('affinity', {})['podAffinity'] = {
        'requiredDuringSchedulingIgnoredDuringExecution': [{
            'topologyKey': 'kubernetes.io/hostname',
            'labelSelector': {},
            'matchLabelKeys': ['pod-template-hash'],
        }],
    }


def m_pdb_both_set(d):
    _pdb(d)['spec']['minAvailable'] = 1  # kube forbids BOTH minAvailable AND maxUnavailable
    _pdb(d)['spec']['maxUnavailable'] = 2


def m_pdb_frac_pct(d):
    _pdb(d)['spec']['minAvailable'] = '50.5%'  # malformed percentage -> must reject cleanly, not traceback


# Each mutation must be REJECTED by check(); m_aff_weight0 also proves the weight=0 (no-op) case is caught.
CASES = [
    m_replicas, m_pdb_min, m_pdb_selector, m_recreate, m_maxunavail_int, m_maxunavail_pct,
    m_aff_hard, m_aff_weight0, m_aff_selector, m_aff_topology, m_pdb_matchexpr, m_aff_matchexpr,
    m_pdb_broad, m_aff_broad, m_maxsurge0, m_pod_identity,
    m_pdb_narrower, m_aff_namespaces, m_aff_nsselector, m_aff_matchlabelkeys,
    m_aff_invalid_op, m_aff_in_empty, m_aff_notin_empty,
    m_nodename, m_nodeselector_host, m_nodeaffinity_host, m_podaffinity_colocate,
    m_paused, m_weight_str, m_pdb_maxunavail_0, m_pdb_maxunavail_2,
    m_podaffinity_empty_selector, m_podaffinity_matchlabelkeys, m_pdb_both_set, m_pdb_frac_pct,
]


def main(argv):
    if len(argv) != 2:
        print("usage: verify_ha_topology_selftest.py <known-good-render.yaml>", file=sys.stderr)
        return 2
    with open(argv[1]) as f:
        base = [d for d in yaml.safe_load_all(f) if d]

    fails = []
    # The unmutated base must PASS — guards against a checker that rejects everything (which would make the
    # negative controls below trivially "pass").
    if check(copy.deepcopy(base)):
        fails.append("BASE render REJECTED — the checker rejects even a good render")
    for fn in CASES:
        docs = copy.deepcopy(base)
        fn(docs)
        if not check(docs):
            fails.append(f"{fn.__name__}: a broken render was ACCEPTED (the guard is missing/ineffective)")

    if fails:
        print("SELF-TEST FAILED:\n  - " + "\n  - ".join(fails))
        return 1
    print(f"HA topology checker self-test OK ({len(CASES)} negative controls rejected, base accepted)")
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv))
