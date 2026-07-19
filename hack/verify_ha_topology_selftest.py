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


# Each mutation must be REJECTED by check(); m_aff_weight0 also proves the weight=0 (no-op) case is caught.
CASES = [
    m_replicas, m_pdb_min, m_pdb_selector, m_recreate, m_maxunavail_int, m_maxunavail_pct,
    m_aff_hard, m_aff_weight0, m_aff_selector, m_aff_topology, m_pdb_matchexpr, m_aff_matchexpr,
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
