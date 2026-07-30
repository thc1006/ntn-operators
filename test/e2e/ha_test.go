//go:build e2e_ha

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// This file is the HA suite (#230) for package e2e. It exercises the active-passive HA behaviours the
// design in docs/high-availability.md relies on, against a chart-deployed 2-replica release (leader
// election on) on a Kind cluster. It uses a SEPARATE build tag (e2e_ha) from the kustomize single-replica
// Ginkgo suite (e2e), so `go test -tags=e2e_ha` compiles only this file. Run via `make test-e2e-ha`.
//
// Namespace comes from HA_NAMESPACE (default ntn-operators-system, the helm-deploy default). The lease is
// the controller-runtime leader-election Lease named after LeaderElectionID in cmd/main.go.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	// haLeaseName must equal LeaderElectionID in cmd/main.go. If it drifts, leaseHolder() fails loudly
	// (Lease not found) rather than passing wrongly. The lease DURATION is NOT hard-coded here — it is read
	// from the live Lease (leaseDuration()) so the RTO bounds track the actual running config, not a copy.
	haLeaseName     = "b1076767.operators.dev"
	managerSelector = "control-plane=controller-manager"
	haReplicas      = 2 // active-passive: one active reconciler + one warm standby
	crdAPIGroup     = "ntn.operators.dev"
)

func haNamespace() string {
	if ns := os.Getenv("HA_NAMESPACE"); ns != "" {
		return ns
	}
	return "ntn-operators-system"
}

// haEnv bundles the clients + namespace for the suite.
type haEnv struct {
	cs  *kubernetes.Clientset
	dyn dynamic.Interface
	ns  string
	ctx context.Context
}

func newHAEnv(t *testing.T) *haEnv {
	t.Helper()
	// Hard opt-in: this suite is DESTRUCTIVE (cordons every node, deletes/evicts manager pods, rewrites a
	// cluster-scoped ClusterRole). Refuse to run unless the caller explicitly acknowledges that — so a stray
	// `make test-e2e-ha` against the wrong kubecontext cannot cordon a production fleet or break a live operator.
	if os.Getenv("HA_ALLOW_DESTRUCTIVE") != "1" {
		msg := "HA suite is DESTRUCTIVE (cordons every node, evicts manager pods, mutates cluster RBAC); " +
			"set HA_ALLOW_DESTRUCTIVE=1 and point KUBECONFIG at a disposable Kind cluster to run it"
		// In CI (GitHub sets CI=true) or when the caller pins HA_REQUIRE_RUN=1, a missing opt-in is a CONFIG
		// error, NOT a reason to skip-then-green: if the workflow ever dropped HA_ALLOW_DESTRUCTIVE, silently
		// skipping every HA test would report success while running nothing. Fail loudly there; skip only for
		// an interactive local run.
		if os.Getenv("CI") == "true" || os.Getenv("HA_REQUIRE_RUN") == "1" {
			t.Fatal(msg + " (refusing to skip under CI/HA_REQUIRE_RUN — that would be a false green)")
		}
		t.Skip(msg)
	}
	cfg, err := ctrlcfg.GetConfig()
	if err != nil {
		t.Fatalf("load kube config (is KUBECONFIG set to the Kind cluster?): %v", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}
	e := &haEnv{cs: cs, dyn: dyn, ns: haNamespace(), ctx: context.Background()}
	e.requireDisposableCluster(t)
	return e
}

// requireDisposableCluster refuses to run unless EVERY node is a Kind node (providerID "kind://…"). The PDB
// test cordons all nodes and the cache-sync test rewrites a cluster-scoped ClusterRole, so a wrong kubecontext
// could cordon a production fleet or break a live operator's RBAC. Set HA_EXPECT_KIND=0 to bypass ONLY when
// you are certain the target is disposable (e.g. a non-Kind ephemeral test cluster).
func (e *haEnv) requireDisposableCluster(t *testing.T) {
	t.Helper()
	if os.Getenv("HA_EXPECT_KIND") == "0" {
		t.Log("HA_EXPECT_KIND=0 — skipping the Kind safety check; caller asserts the cluster is disposable")
		return
	}
	nodes, err := e.cs.CoreV1().Nodes().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list nodes for the disposable-cluster safety check: %v", err)
	}
	if len(nodes.Items) == 0 {
		t.Fatal("no nodes found — refusing to run the destructive HA suite")
	}
	for i := range nodes.Items {
		if pid := nodes.Items[i].Spec.ProviderID; !strings.HasPrefix(pid, "kind://") {
			t.Fatalf("node %s is not a Kind node (providerID=%q) — refusing to cordon/mutate a non-disposable "+
				"cluster; set HA_EXPECT_KIND=0 only if you are certain the target is disposable",
				nodes.Items[i].Name, pid)
		}
	}
}

// eventually polls cond until it returns (true, _) or timeout; on timeout it fails with the last message.
func eventually(t *testing.T, timeout, interval time.Duration, desc string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for {
		ok, msg := cond()
		if ok {
			return
		}
		last = msg
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s; last: %s", timeout, desc, last)
		}
		time.Sleep(interval)
	}
}

// leaseHolder returns the current leader-election Lease holderIdentity ("" if unset).
func (e *haEnv) leaseHolder() (string, error) {
	l, err := e.cs.CoordinationV1().Leases(e.ns).Get(e.ctx, haLeaseName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if l.Spec.HolderIdentity == nil {
		return "", nil
	}
	return *l.Spec.HolderIdentity, nil
}

// leaseDuration reads the deployed leader-election Lease's configured duration, so RTO bounds track the
// ACTUAL running config (cmd/main.go's leaderLeaseDuration) instead of a hard-coded copy that could drift.
func (e *haEnv) leaseDuration(t *testing.T) time.Duration {
	t.Helper()
	l, err := e.cs.CoordinationV1().Leases(e.ns).Get(e.ctx, haLeaseName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get lease %s for its duration: %v", haLeaseName, err)
	}
	if l.Spec.LeaseDurationSeconds == nil {
		t.Fatalf("lease %s has no leaseDurationSeconds", haLeaseName)
	}
	ld := time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second
	// Pin the ABSOLUTE HA failover budget. The RTO bounds derived from ld are self-calibrating (relative to
	// it), so they would silently scale with a leaderLeaseDuration regression — 15s -> 120s makes an 8x-slower
	// handoff still "pass". Assert the deployed lease stays within the stated sub-15s active-passive budget
	// (docs/high-availability.md; the chart-invariants CI step pins the source constant too).
	if ld > 15*time.Second {
		t.Fatalf("deployed lease duration %s exceeds the pinned 15s HA failover budget", ld)
	}
	return ld
}

// holderPodBase maps a controller-runtime holderIdentity ("<pod>_<uuid>") to its owning pod name.
func holderPodBase(holder string) string {
	if i := strings.LastIndex(holder, "_"); i > 0 {
		return holder[:i]
	}
	return holder
}

// managerContainerID returns the containerd id of the pod's "manager" container (the chart's container name),
// stripped of the "containerd://" scheme — selected BY NAME, not container index 0, so it stays correct if a
// sidecar is ever added.
func managerContainerID(pod *corev1.Pod) (string, bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == "manager" && cs.ContainerID != "" {
			return strings.TrimPrefix(cs.ContainerID, "containerd://"), true
		}
	}
	return "", false
}

// managerPods lists the controller-manager pods.
func (e *haEnv) managerPods() ([]corev1.Pod, error) {
	pl, err := e.cs.CoreV1().Pods(e.ns).List(e.ctx, metav1.ListOptions{LabelSelector: managerSelector})
	if err != nil {
		return nil, err
	}
	return pl.Items, nil
}

func podReady(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (e *haEnv) countReady() (ready, notReady int, err error) {
	pods, err := e.managerPods()
	if err != nil {
		return 0, 0, err
	}
	for i := range pods {
		if pods[i].DeletionTimestamp != nil {
			continue
		}
		if podReady(&pods[i]) {
			ready++
		} else {
			notReady++
		}
	}
	return ready, notReady, nil
}

// waitReadyManagerPods blocks until exactly want manager pods are Ready (and none extra lingering NotReady).
func (e *haEnv) waitReadyManagerPods(t *testing.T) {
	const want = haReplicas
	t.Helper()
	eventually(t, 4*time.Minute, 3*time.Second, fmt.Sprintf("%d ready manager pods", want), func() (bool, string) {
		ready, notReady, err := e.countReady()
		if err != nil {
			return false, err.Error()
		}
		return ready == want && notReady == 0, fmt.Sprintf("%d ready / %d notReady", ready, notReady)
	})
}

// waitLeaseHolder blocks until the lease has a non-empty holder and returns it.
func (e *haEnv) waitLeaseHolder(t *testing.T) string {
	t.Helper()
	holder := ""
	eventually(t, time.Minute, 2*time.Second, "an initial lease holder", func() (bool, string) {
		h, err := e.leaseHolder()
		if err != nil {
			return false, err.Error()
		}
		holder = h
		return h != "", "holder empty"
	})
	return holder
}

var gslGVR = schema.GroupVersionResource{Group: crdAPIGroup, Version: "v1alpha1", Resource: "groundstationlifecycles"}

// assertLeaderReconciles proves the CURRENT leader actually reconciles (not just holds the lease): it
// creates a GroundStationLifecycle and waits for the controller to populate .status.conditions.
func (e *haEnv) assertLeaderReconciles(t *testing.T, name string) {
	t.Helper()
	gsl := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": crdAPIGroup + "/v1alpha1",
		"kind":       "GroundStationLifecycle",
		"metadata":   map[string]any{"name": name, "namespace": e.ns},
		"spec": map[string]any{
			"hardware":   map[string]any{"vendor": "ennoconn", "model": "edge-5000"},
			"deployment": map[string]any{"location": map[string]any{"lat": "25.0330", "lon": "121.5654"}},
		},
	}}
	if _, err := e.dyn.Resource(gslGVR).Namespace(e.ns).Create(e.ctx, gsl, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create GroundStationLifecycle: %v", err)
	}
	t.Cleanup(func() {
		_ = e.dyn.Resource(gslGVR).Namespace(e.ns).Delete(context.Background(), name, metav1.DeleteOptions{})
	})
	eventually(t, 90*time.Second, 3*time.Second, "leader reconciles a GroundStationLifecycle", func() (bool, string) {
		got, err := e.dyn.Resource(gslGVR).Namespace(e.ns).Get(e.ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
		return len(conds) > 0, fmt.Sprintf("%d status conditions", len(conds))
	})
}

// TestHAGracefulFailover (#230 item 2): deleting the leader pod GRACEFULLY (LeaderElectionReleaseOnCancel
// releases the lease on SIGTERM) must hand the lease to the standby, and the NEW leader must complete a
// real reconcile — not merely win the lease.
func TestHAGracefulFailover(t *testing.T) {
	e := newHAEnv(t)
	e.waitReadyManagerPods(t)
	ld := e.leaseDuration(t)
	holder0 := e.waitLeaseHolder(t)
	// Re-confirm holder0 is STILL the leader right before the delete — leadership could have moved while the
	// prior test's churn settled; deleting a standby would make the measurement meaningless and (with no lower
	// bound) trivially pass. If it moved, re-resolve so we always delete the ACTUAL current leader.
	if h, err := e.leaseHolder(); err == nil && h != holder0 {
		holder0 = e.waitLeaseHolder(t)
	}
	leader0 := holderPodBase(holder0)
	t.Logf("initial leader pod=%s holder=%s", leader0, holder0)

	if err := e.cs.CoreV1().Pods(e.ns).Delete(e.ctx, leader0, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete leader pod: %v", err)
	}
	start := time.Now()
	eventually(t, time.Minute, 200*time.Millisecond, "the lease to fail over to a new holder", func() (bool, string) {
		h, err := e.leaseHolder()
		if err != nil {
			return false, err.Error()
		}
		// Full-identity comparison (not pod-base): the deleted leader is gone, so ANY new holder — the standby
		// or the deleted pod's fresh-named replacement — has a different identity. Stricter than pod-base and
		// closes the "we deleted a standby and instantly saw the already-moved holder" false pass.
		if h == "" || h == holder0 {
			return false, "holder still " + h
		}
		return true, ""
	})
	rto := time.Since(start)
	h1, _ := e.leaseHolder()
	t.Logf("GRACEFUL failover took %s; new holder=%s", rto.Round(time.Millisecond), h1)
	// Upper bound PROVES the lease was ACTIVELY released on SIGTERM (LeaderElectionReleaseOnCancel), not merely
	// that it failed over eventually: an active release hands off within a few RetryPeriod ticks, whereas a
	// regressed release falls back to the LeaseDuration timeout (~15 s). Bound at 2/3 of the lease duration:
	// the standby acquires only on its next client-go acquire tick, which jitters up to RetryPeriod*(1+1.2),
	// so with SIGTERM delivery + graceful shutdown the worst legitimate active release approaches ~9 s on a
	// loaded runner — ld/2 (7.5 s) would false-fail; 2*ld/3 (10 s) absorbs the jitter yet stays clearly below
	// the ~13 s lease-timeout floor (the ungraceful RTO). Without any bound a broken release would still pass.
	if rto >= 2*ld/3 {
		t.Fatalf("graceful failover took %s (>= 2*LeaseDuration/3=%s) — the lease was NOT actively released on "+
			"SIGTERM; it fell back to the timeout path (LeaderElectionReleaseOnCancel regressed?)", rto, 2*ld/3)
	}
	e.waitReadyManagerPods(t)
	e.assertLeaderReconciles(t, "ha-graceful-probe")
}

// ungracefulSkipOrFail skips the ungraceful test locally, but FAILS it under HA_REQUIRE_UNGRACEFUL=1 — a CI
// job that claims to exercise ungraceful (SIGKILL) failover must actually run it, never skip-then-green when
// docker/crictl/Kind integration regresses.
func ungracefulSkipOrFail(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("HA_REQUIRE_UNGRACEFUL") == "1" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

// TestHAUngracefulFailover (#230 item 3): an UNGRACEFUL leader loss (the container SIGKILLed on the node, so
// the process never runs the lease-release defer) must still fail over — the standby waits out LeaseDuration
// — and the RTO is recorded. A plain pod delete cannot exercise this: the manager image is distroless, so a
// pod delete routes through the kubelet's SIGTERM and the manager releases the lease in ~1 s (that is the
// GRACEFUL path, covered above). To force the no-release path we SIGKILL the container directly via crictl on
// the Kind node. Requires docker + a Kind node; skipped otherwise. The lower-bound assertion PROVES the
// lease-timeout path was taken (a graceful release would be ~1 s), not just that failover eventually happened.
func TestHAUngracefulFailover(t *testing.T) {
	e := newHAEnv(t)
	e.waitReadyManagerPods(t)
	ld := e.leaseDuration(t) // self-calibrating: RTO bounds derive from the live Lease, not a hard-coded copy

	if _, err := exec.LookPath("docker"); err != nil {
		ungracefulSkipOrFail(t, "docker not available: the ungraceful (node-level SIGKILL) test needs a Kind cluster")
	}

	// Resolve the CURRENT leader and SIGKILL its container, RETRYING if leadership moves during resolution — a
	// stale target would make the RTO meaningless. Leadership is stable in steady state, so attempt 0 almost
	// always wins; we retry rather than skip so a genuine environment regression cannot hide behind a skip. A
	// missing docker or a failed crictl is an environment fault, not a race, so it goes through
	// ungracefulSkipOrFail (fatal under HA_REQUIRE_UNGRACEFUL, skip otherwise).
	var holder0 string
	var start time.Time
	killed := false
	for attempt := 0; attempt < 3 && !killed; attempt++ {
		holder0 = e.waitLeaseHolder(t)
		leader0 := holderPodBase(holder0)
		pod, err := e.cs.CoreV1().Pods(e.ns).Get(e.ctx, leader0, metav1.GetOptions{})
		if err != nil {
			t.Logf("attempt %d: get leader pod %s: %v — re-resolving", attempt, leader0, err)
			continue
		}
		cid, ok := managerContainerID(pod)
		if !ok {
			t.Logf("attempt %d: leader pod %s has no running 'manager' container id — re-resolving", attempt, leader0)
			continue
		}
		node := pod.Spec.NodeName
		// Re-confirm this is STILL the leader immediately before the kill; only a SUCCESSFUL read showing a
		// different holder counts as "moved" (a transient API error must not masquerade as a move and burn a
		// retry — that would turn flakiness into a HA_REQUIRE_UNGRACEFUL Fatal).
		if h, err := e.leaseHolder(); err == nil && h != holder0 {
			t.Logf("attempt %d: leadership moved during resolution (%s -> %s) — re-resolving", attempt, holder0, h)
			continue
		}
		t.Logf("attempt %d: SIGKILL leader pod=%s node=%s container=%.12s", attempt, leader0, node, cid)
		// crictl stop --timeout 0 SIGKILLs the container immediately — no SIGTERM, so no lease release.
		kill := exec.Command("docker", "exec", node, "crictl", "stop", "--timeout", "0", cid)
		if out, err := kill.CombinedOutput(); err != nil {
			ungracefulSkipOrFail(t, "could not SIGKILL the container via docker/crictl (non-Kind cluster?): %v: %s", err, out)
		}
		start = time.Now() // measure the RTO from the fault, NOT including the docker-exec setup
		killed = true
	}
	if !killed {
		ungracefulSkipOrFail(t, "leadership kept moving before the kill across 3 attempts; cannot measure RTO")
	}

	eventually(t, ld+30*time.Second, time.Second, "ungraceful failover", func() (bool, string) {
		h, err := e.leaseHolder()
		if err != nil {
			return false, err.Error()
		}
		// A crictl SIGKILL restarts the container in the SAME pod, which can re-acquire the lease before the
		// standby — but any re-acquire mints a FRESH holderIdentity (a new uuid), so compare the full identity,
		// not just the pod name. This detects the lease-timeout recovery whether the standby wins or the
		// restarted leader does; the RTO is ~LeaseDuration either way.
		if h == "" || h == holder0 {
			return false, "holder still " + h
		}
		return true, ""
	})
	rto := time.Since(start)
	t.Logf("ungraceful (SIGKILL) failover RTO=%s (LeaseDuration=%s)", rto.Round(time.Millisecond), ld)

	// The lease renews every few seconds, so the standby cannot steal it until ~LeaseDuration after the last
	// renew. A graceful release would be ~1 s; require the RTO to be clearly in the lease-timeout regime
	// (>= half the lease duration), proving no release happened, while bounding the top for a slow CI.
	if rto < ld/2 {
		t.Fatalf("ungraceful RTO %s too fast (<LeaseDuration/2=%s) — released, not the lease-timeout path", rto, ld/2)
	}
	// Upper bound kept below the eventually window (LeaseDuration+30s) so a legitimately-slow-but-bounded
	// failover trips THIS clear message rather than the generic eventually timeout.
	if rto > ld+20*time.Second {
		t.Fatalf("ungraceful failover RTO %s exceeded LeaseDuration+margin (%s)", rto, ld+20*time.Second)
	}
	e.waitReadyManagerPods(t)
}

// TestHAPDBEviction (#230 item 5): the PDB (minAvailable=1 over 2 replicas) must let a voluntary disruption
// evict at most ONE manager pod at a time. Cordon the node(s) so an evicted pod's replacement cannot become
// Ready, then evict both: exactly one eviction succeeds and the other is refused by the PDB.
func TestHAPDBEviction(t *testing.T) {
	e := newHAEnv(t)
	e.waitReadyManagerPods(t)
	pods, err := e.managerPods()
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	var ready []string
	for i := range pods {
		if podReady(&pods[i]) {
			ready = append(ready, pods[i].Name)
		}
	}
	if len(ready) != 2 {
		t.Fatalf("need exactly 2 ready manager pods, got %d", len(ready))
	}

	// Cordon every node so an evicted pod's replacement stays Pending (never Ready), making the PDB's
	// currentHealthy accounting deterministic.
	nodes, err := e.cs.CoreV1().Nodes().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	// Record each node's ORIGINAL schedulability so cleanup restores exactly that, rather than unconditionally
	// uncordoning — never wrongly un-cordoning a node an operator had deliberately cordoned (matters for a
	// non-Kind disposable cluster under HA_EXPECT_KIND=0; a fresh Kind node is schedulable anyway).
	origUnsched := make(map[string]bool, len(nodes.Items))
	for i := range nodes.Items {
		origUnsched[nodes.Items[i].Name] = nodes.Items[i].Spec.Unschedulable
	}
	// Register the restore cleanup BEFORE cordoning, so a mid-loop cordon failure (Fatal) still restores the
	// nodes already changed. A cordon that SILENTLY failed would let the replacement become Ready and the
	// second eviction be legitimately allowed — which we would then misreport as "PDB unprotected" — so a
	// SETUP cordon failure is Fatal; the cleanup restore is best-effort but reported.
	t.Cleanup(func() {
		var stuck []string
		for i := range nodes.Items {
			n := nodes.Items[i].Name
			// Retry: a single failed uncordon would leave the node unschedulable and hang the NEXT test's
			// waitReadyManagerPods for its full 4-minute budget (on a single-node Kind, wedging the suite).
			var err error
			for range 5 {
				if err = e.setCordon(n, origUnsched[n]); err == nil {
					break
				}
				time.Sleep(time.Second)
			}
			if err != nil {
				stuck = append(stuck, n)
				t.Logf("cleanup: restore node %s unschedulable=%v failed after retries: %v", n, origUnsched[n], err)
			}
		}
		if len(stuck) > 0 {
			t.Errorf("CRITICAL: cleanup could not restore node schedulability for %v", stuck)
		}
	})
	for i := range nodes.Items {
		if origUnsched[nodes.Items[i].Name] {
			continue // already cordoned by its operator; leave it (and cleanup leaves it) untouched
		}
		if err := e.setCordon(nodes.Items[i].Name, true); err != nil {
			t.Fatalf("setup: cordon node %s failed: %v", nodes.Items[i].Name, err)
		}
	}

	// Wait for the PDB status to CATCH UP before evicting: the disruption controller recomputes
	// disruptionsAllowed asynchronously and lags pod-Ready (this test runs right after the SIGKILL test churns
	// a pod), so an eviction in that lag window would 429 and false-fail "first eviction should succeed". This
	// also cross-checks the selector at runtime: expectedPods must equal exactly the manager replica count (a
	// stray pod sharing the manager label would inflate it — the runtime complement to the checker's
	// structural over-broad guard).
	e.waitManagerPDBReady(t)

	evict := func(pod string) error {
		return e.cs.PolicyV1().Evictions(e.ns).Evict(e.ctx, &policyv1.Eviction{
			ObjectMeta: metav1.ObjectMeta{Name: pod, Namespace: e.ns},
		})
	}
	// First eviction must succeed (2 healthy → 1, still >= minAvailable=1).
	if err := evict(ready[0]); err != nil {
		t.Fatalf("first eviction of %s should succeed (2 healthy → 1 >= minAvailable); got: %v", ready[0], err)
	}
	t.Logf("evicted %s (first, allowed)", ready[0])
	// The second eviction must be REFUSED by the PDB (evicting it would drop below minAvailable=1). The
	// replacement cannot become Ready (cordoned), so currentHealthy stays 1. Retry ONLY on a transient
	// error (the PDB status may lag the first eviction by a poll); an ALLOWED eviction — or a later NotFound
	// proving a prior attempt was wrongly allowed — is a genuine PDB regression and must fail FAST with a
	// clear message, not be masked as a timeout after we've silently destroyed the last replica.
	refused := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		err := evict(ready[1])
		switch {
		case err == nil:
			t.Fatalf("PDB ALLOWED the second eviction of %s — the last replica was left unprotected", ready[1])
		case apierrors.IsTooManyRequests(err) && strings.Contains(err.Error(), "disruption budget"):
			// Require the disruption-budget MESSAGE, not a bare 429 — an unrelated 429 (e.g. API-priority
			// throttling) must not be misread as a PDB refusal.
			refused = true
		case apierrors.IsNotFound(err):
			t.Fatalf("second pod %s is gone — a prior eviction was wrongly allowed (PDB unprotected)", ready[1])
		default:
			time.Sleep(2 * time.Second) // transient (e.g. PDB currentHealthy not yet recomputed); retry
			continue
		}
		break
	}
	if !refused {
		t.Fatal("timed out waiting for the PDB to refuse the second eviction")
	}
	t.Logf("second eviction of %s correctly REFUSED by the PDB", ready[1])
}

func (e *haEnv) setCordon(node string, unschedulable bool) error {
	patch := fmt.Sprintf(`{"spec":{"unschedulable":%v}}`, unschedulable)
	_, err := e.cs.CoreV1().Nodes().Patch(e.ctx, node, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// waitManagerPDBReady waits for the single manager PodDisruptionBudget's status to CATCH UP with 2 healthy
// replicas — disruptionsAllowed >= 1 (the disruption controller computes this asynchronously and lags
// pod-Ready, so evicting too early would 429) — and cross-checks expectedPods == haReplicas: the selector must
// resolve to EXACTLY the manager replicas, so a runtime stray sharing the manager label (which the structural
// checker cannot see) fails here.
func (e *haEnv) waitManagerPDBReady(t *testing.T) {
	t.Helper()
	eventually(t, 60*time.Second, time.Second, "manager PDB to allow one disruption", func() (bool, string) {
		l, err := e.cs.PolicyV1().PodDisruptionBudgets(e.ns).List(e.ctx, metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		if len(l.Items) != 1 {
			t.Fatalf("expected exactly 1 PodDisruptionBudget in %s, got %d", e.ns, len(l.Items))
		}
		s := l.Items[0].Status
		if s.ExpectedPods > int32(haReplicas) {
			t.Fatalf("PDB selects %d pods, more than the %d manager replicas — over-broad selector (matches strays)",
				s.ExpectedPods, haReplicas)
		}
		ok := s.ExpectedPods == int32(haReplicas) && s.CurrentHealthy == int32(haReplicas) && s.DisruptionsAllowed >= 1
		return ok, fmt.Sprintf("expectedPods=%d currentHealthy=%d disruptionsAllowed=%d",
			s.ExpectedPods, s.CurrentHealthy, s.DisruptionsAllowed)
	})
}

// TestHACacheSyncRolloutGate (#230 item 4): /readyz gates on informer cache-sync, not leadership (#226). A
// replica whose cache cannot sync must NOT become Ready, so a rollout of such a replica must NOT tear down
// the healthy old version. We break the manager ClusterRole's list/watch on the CRD group, force a rollout,
// and assert that a new NotReady pod appears while ≥1 Ready pod is preserved throughout; then restore.
func TestHACacheSyncRolloutGate(t *testing.T) {
	e := newHAEnv(t)
	e.waitReadyManagerPods(t)

	crName := e.managerClusterRoleName(t)
	orig, err := e.cs.RbacV1().ClusterRoles().Get(e.ctx, crName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRole %s: %v", crName, err)
	}
	// Guard against an already-broken baseline (a prior aborted run may have left list/watch stripped): if we
	// captured a broken ClusterRole as "orig", restoring to it would LOCK IN the breakage. Require the baseline
	// to currently grant list+watch on the CRD group before we break it.
	if !clusterRoleGrantsCRDListWatch(orig) {
		t.Fatalf("ClusterRole %s does not currently grant list+watch on %s — the baseline looks already broken; "+
			"redeploy the chart (make helm-deploy) before running the HA suite", crName, crdAPIGroup)
	}
	deploy := e.deployName(t)

	// Reliable, idempotent restore: retry on conflict, and mark "restored" ONLY after BOTH the RBAC update and
	// the rollout patch succeed — so a transient API error on the in-body restore does not consume the attempt
	// and leave the ClusterRole permanently broken (the t.Cleanup failsafe then still retries). The in-body
	// caller fails the test on error; the cleanup reports a CRITICAL if it ultimately cannot restore.
	var restoreMu sync.Mutex
	restored := false
	restore := func() error {
		restoreMu.Lock()
		defer restoreMu.Unlock()
		if restored {
			return nil
		}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cur, err := e.cs.RbacV1().ClusterRoles().Get(context.Background(), crName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			cur.Rules = orig.Rules
			_, err = e.cs.RbacV1().ClusterRoles().Update(context.Background(), cur, metav1.UpdateOptions{})
			return err
		}); err != nil {
			return fmt.Errorf("restore ClusterRole %s: %w", crName, err)
		}
		if _, err := e.cs.AppsV1().Deployments(e.ns).Patch(context.Background(), deploy,
			types.StrategicMergePatchType, []byte(rolloutRestartPatch()), metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("rollout restart after RBAC restore: %w", err)
		}
		restored = true
		return nil
	}
	t.Cleanup(func() {
		if err := restore(); err != nil {
			t.Errorf("CRITICAL: cleanup could not restore the manager ClusterRole/rollout — the cluster may be "+
				"left with broken RBAC: %v", err)
		}
	})

	// Record the pre-rollout pod-template-hashes: the healthy (old) generation the gate must NOT tear down, and
	// the baseline for identifying a NEW-generation (rolled) pod below.
	oldHashes := map[string]bool{}
	if pods, err := e.managerPods(); err == nil {
		for i := range pods {
			if h := pods[i].Labels["pod-template-hash"]; h != "" {
				oldHashes[h] = true
			}
		}
	}
	if len(oldHashes) == 0 {
		t.Fatal("could not record any pre-rollout pod-template-hash — cannot distinguish new pods from old")
	}

	// Strip list/watch on the CRD group so a fresh informer cannot sync those caches.
	//
	// WHY this holds a STANDBY NotReady (load-bearing, keep this in sync with the controller): the surge/rolled
	// pod is a non-leader standby, and controllers are leader-election runnables, so their CRD informers are
	// normally NOT created on a standby. The reason a standby's cache-sync /readyz gate is observable at all is
	// that NTNCellConfigReconciler.SetupWithManager calls mgr.GetFieldIndexer().IndexField(...), which EAGERLY
	// creates the NTNCellConfig informer on EVERY replica at setup — and controller-runtime waits on it
	// (runnables.Caches -> WaitForCacheSync) before the non-leader cacheReadyRunnable flips /readyz ready.
	// Breaking list/watch on ntn.operators.dev stalls that lone informer -> /readyz 500 -> standby NotReady.
	// If that eager field indexer is ever removed, a standby would sync its (empty) CRD cache and go Ready under
	// broken RBAC, flipping this test to a confusing false-FAIL with no /readyz change — revisit this then.
	broken := orig.DeepCopy()
	for i := range broken.Rules {
		r := &broken.Rules[i]
		if ruleHasGroup(r, crdAPIGroup) {
			r.Verbs = withoutVerbs(r.Verbs, "list", "watch")
		}
	}
	if _, err := e.cs.RbacV1().ClusterRoles().Update(e.ctx, broken, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("break ClusterRole: %v", err)
	}
	// Force a rollout so new-generation pods start with the broken RBAC.
	if _, err := e.cs.AppsV1().Deployments(e.ns).Patch(e.ctx, deploy,
		types.StrategicMergePatchType, []byte(rolloutRestartPatch()), metav1.PatchOptions{}); err != nil {
		t.Fatalf("rollout restart: %v", err)
	}

	// Proving the gate needs MORE than a single "Running+NotReady" poll: every healthy pod is briefly
	// Running-but-NotReady during startup (the chart readiness probe has a 5 s initialDelay), so one such poll
	// could be a transient startup blip, not sustained gating. Instead: (1) wait for the surge pod, failing if
	// any new-generation pod is already Ready; (2) confirm via /readyz?verbose that the /readyz (cache-sync)
	// gate is what's failing, not a crash or unrelated probe; (3) hold for a sustained window (>> the ~15 s a
	// healthy pod needs to go Ready) asserting the SAME pod stays NotReady, NO new-generation pod EVER goes
	// Ready, and a Ready old-generation pod is preserved (maxUnavailable=0); (4) re-assert NotReady at the end.
	gatedName, gatedUID := e.waitForGatedNewPod(t, oldHashes)
	e.assertReadyzGateFailing(t, gatedName)

	const sustain = 45 * time.Second
	minReady := 1 << 30
	polls, heldPolls, errPolls := 0, 0, 0
	hold := time.Now().Add(sustain)
	for time.Now().Before(hold) {
		time.Sleep(time.Second)
		pods, err := e.managerPods()
		if err != nil {
			errPolls++ // count separately so a run of API errors reports as flakiness, not "gate not holding"
			continue
		}
		polls++
		ready, gatedHeld, newReady := scanSustained(pods, oldHashes, gatedUID)
		if newReady != "" {
			t.Fatalf("new-generation pod %s became Ready under broken cache RBAC — the cache-sync gate did NOT "+
				"hold (a healthy pod goes Ready; this is exactly the transient-startup false pass a single-poll "+
				"check would have missed)", newReady)
		}
		if ready < minReady {
			minReady = ready
		}
		if gatedHeld {
			heldPolls++
		}
	}
	if polls == 0 {
		t.Fatalf("could not observe the gate — every one of %d polls hit an API error", errPolls)
	}
	if heldPolls < polls {
		t.Fatalf("gated pod %s was not continuously present+NotReady across the %s window (%d/%d good polls, %d "+
			"API errors) — not sustained gating", gatedName, sustain, heldPolls, polls, errPolls)
	}
	// maxUnavailable=0 must keep BOTH old replicas Ready while the new one is gated — a regression to
	// maxUnavailable=1 (one old pod torn down) leaves min ready=1, which "< haReplicas" catches but "< 1" would
	// silently pass. The old pods' cachesSynced latch + readiness failureThreshold=3 keep this stable.
	if minReady < haReplicas {
		t.Fatalf("fewer than %d healthy pods during the broken rollout (min ready=%d) — the maxUnavailable=0 "+
			"guarantee failed (a healthy old replica was torn down)", haReplicas, minReady)
	}
	// (4) final re-assert: the gated pod is STILL NotReady, not a blip that recovered.
	final, err := e.cs.CoreV1().Pods(e.ns).Get(e.ctx, gatedName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get gated pod %s at end: %v", gatedName, err)
	}
	if podReady(final) {
		t.Fatalf("gated pod %s became Ready by the end of the window — the gate did not hold", gatedName)
	}
	t.Logf("cache-sync /readyz gate held: %s stayed NotReady (confirmed via the /readyz gate) across %s, no "+
		"new-generation pod ever went Ready, min ready=%d", gatedName, sustain, minReady)

	// Restore RBAC + roll again, and confirm full recovery (the Cleanup is the failsafe).
	if err := restore(); err != nil {
		t.Fatalf("restore RBAC + roll: %v", err)
	}
	e.waitReadyManagerPods(t)
	t.Log("recovered: 2 ready manager pods after restoring RBAC")
}

// scanSustained summarizes one poll of the sustained cache-sync window: ready = count of Ready pods;
// gatedHeld = the tracked gated pod (by UID) is present and NotReady; newReady = the name of a NEW-generation
// pod that has (wrongly) become Ready, or "" if none. Extracted to keep the test's cyclomatic complexity down.
func scanSustained(
	pods []corev1.Pod, oldHashes map[string]bool, gatedUID types.UID,
) (ready int, gatedHeld bool, newReady string) {
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil {
			continue
		}
		isNew := false
		if h := p.Labels["pod-template-hash"]; h != "" && !oldHashes[h] {
			isNew = true
		}
		if podReady(p) {
			ready++
			if isNew {
				newReady = p.Name
			}
			continue
		}
		if p.UID == gatedUID {
			gatedHeld = true
		}
	}
	return ready, gatedHeld, newReady
}

// waitForGatedNewPod waits for a NEW-generation manager pod (a pod-template-hash absent before the rollout) to
// appear Running-but-NotReady — the surge pod the cache-sync gate should hold — and returns its name + UID. It
// FAILS immediately if a new-generation pod is already Ready: a healthy pod goes Ready, so a Ready new pod
// means the gate is not holding.
func (e *haEnv) waitForGatedNewPod(t *testing.T, oldHashes map[string]bool) (string, types.UID) {
	t.Helper()
	var name string
	var uid types.UID
	eventually(t, 60*time.Second, time.Second, "a new-generation pod held NotReady by the rollout", func() (bool, string) {
		pods, err := e.managerPods()
		if err != nil {
			return false, err.Error()
		}
		for i := range pods {
			p := &pods[i]
			if p.DeletionTimestamp != nil {
				continue
			}
			h := p.Labels["pod-template-hash"]
			if h == "" || oldHashes[h] {
				continue
			}
			if podReady(p) {
				t.Fatalf("new-generation pod %s is Ready under broken cache RBAC — the cache-sync gate is not holding", p.Name)
			}
			if containerRunning(p) {
				name, uid = p.Name, p.UID
				return true, ""
			}
		}
		return false, "no running new-generation pod yet"
	})
	return name, uid
}

// assertReadyzGateFailing confirms the pod is NotReady BECAUSE the /readyz cache-sync gate is failing — not a
// crash or an unrelated probe — by reading /readyz?verbose through the API-server pod proxy. The manager's ONLY
// readyz check is the cache-sync gate (cmd/main.go registers "readyz" = cacheSyncReadyCheck), so a failing
// "readyz" check is the cache-sync gate holding the pod NotReady.
func (e *haEnv) assertReadyzGateFailing(t *testing.T, pod string) {
	t.Helper()
	eventually(t, 30*time.Second, 2*time.Second, "/readyz cache-sync gate to report failing", func() (bool, string) {
		// /readyz returns HTTP 500 while not ready, so DoRaw sets err for the non-2xx; the verbose body is
		// still returned in raw. Only a truly empty body is a transport problem worth retrying.
		raw, err := e.cs.CoreV1().RESTClient().Get().
			Namespace(e.ns).Resource("pods").SubResource("proxy").
			Name(pod+":8081").Suffix("readyz").Param("verbose", "").
			DoRaw(e.ctx)
		if len(raw) == 0 {
			if err != nil {
				return false, err.Error()
			}
			return false, "empty /readyz body"
		}
		// The verbose body lists each check on its own line: "[+]<name> ok" when passing, "[-]<name> failed:
		// reason withheld" when failing (controller-runtime's summary line is always "healthz check failed",
		// even for the readyz endpoint, so we match the per-check "[-]readyz" line — the specific, authoritative
		// signal that our cache-sync gate is the one holding the pod NotReady).
		body := string(raw)
		if strings.Contains(body, "[-]readyz") {
			return true, ""
		}
		return false, "readyz did not report the cache-sync gate failing: " + body
	})
	t.Logf("confirmed %s is held NotReady by the /readyz cache-sync gate", pod)
}

// containerRunning reports whether any of the pod's containers is currently in the Running state — used to
// distinguish "started but held NotReady by the readiness gate" from a pull/schedule/create failure.
func containerRunning(p *corev1.Pod) bool {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Running != nil {
			return true
		}
	}
	return false
}

// clusterRoleGrantsCRDListWatch reports whether the ClusterRole grants list AND watch on the CRD API group.
func clusterRoleGrantsCRDListWatch(cr *rbacv1.ClusterRole) bool {
	for i := range cr.Rules {
		if ruleHasGroup(&cr.Rules[i], crdAPIGroup) && ruleGrantsVerbs(&cr.Rules[i], "list", "watch") {
			return true
		}
	}
	return false
}

func (e *haEnv) deployName(t *testing.T) string {
	t.Helper()
	dl, err := e.cs.AppsV1().Deployments(e.ns).List(e.ctx, metav1.ListOptions{LabelSelector: managerSelector})
	if err != nil {
		t.Fatalf("list manager Deployments: %v", err)
	}
	if len(dl.Items) != 1 {
		t.Fatalf("expected exactly 1 manager Deployment (%s in %s), got %d", managerSelector, e.ns, len(dl.Items))
	}
	return dl.Items[0].Name
}

// managerClusterRoleName resolves the manager's ClusterRole PRECISELY — from a manager pod's ServiceAccount
// through the ClusterRoleBindings that bind it to their roleRef — rather than scanning every ClusterRole by a
// name substring, so a multi-release or multi-operator cluster cannot lead us to break the wrong role. Among
// the ClusterRoles bound to the SA it returns the one that actually grants list+watch on the CRD group (the
// informer's RBAC), and fails if that is ambiguous.
func (e *haEnv) managerClusterRoleName(t *testing.T) string {
	t.Helper()
	pods, err := e.managerPods()
	if err != nil || len(pods) == 0 {
		t.Fatalf("find a manager pod to resolve its ServiceAccount: err=%v n=%d", err, len(pods))
	}
	sa := pods[0].Spec.ServiceAccountName
	if sa == "" {
		t.Fatalf("manager pod %s has no ServiceAccountName", pods[0].Name)
	}
	crbs, err := e.cs.RbacV1().ClusterRoleBindings().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list ClusterRoleBindings: %v", err)
	}
	// Collect the ClusterRoles bound to the manager SA.
	var bound []string
	for i := range crbs.Items {
		crb := &crbs.Items[i]
		if crb.RoleRef.Kind != "ClusterRole" {
			continue
		}
		for _, s := range crb.Subjects {
			if s.Kind == "ServiceAccount" && s.Name == sa && s.Namespace == e.ns {
				bound = append(bound, crb.RoleRef.Name)
				break
			}
		}
	}
	// Pick the bound role that grants list+watch on the CRD group; that uniquely identifies the informer role.
	match := ""
	for _, name := range bound {
		cr, err := e.cs.RbacV1().ClusterRoles().Get(e.ctx, name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		if clusterRoleGrantsCRDListWatch(cr) {
			if match != "" && match != name {
				t.Fatalf("ambiguous: ClusterRoles %s and %s (both bound to SA %s) grant list+watch on %s",
					match, name, sa, crdAPIGroup)
			}
			match = name
		}
	}
	if match == "" {
		t.Fatalf("no ClusterRole bound to SA %s grants list+watch on %s (bound roles: %v)", sa, crdAPIGroup, bound)
	}
	return match
}

func ruleHasGroup(r *rbacv1.PolicyRule, group string) bool {
	for _, g := range r.APIGroups {
		if g == group || g == "*" {
			return true
		}
	}
	return false
}

// ruleGrantsVerbs reports whether the rule grants EVERY listed verb (treating "*" as all verbs).
func ruleGrantsVerbs(r *rbacv1.PolicyRule, verbs ...string) bool {
	has := func(want string) bool {
		for _, v := range r.Verbs {
			if v == want || v == "*" {
				return true
			}
		}
		return false
	}
	for _, want := range verbs {
		if !has(want) {
			return false
		}
	}
	return true
}

func withoutVerbs(vs []string, drop ...string) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		keep := v != "*"
		for _, d := range drop {
			if v == d {
				keep = false
			}
		}
		if keep {
			out = append(out, v)
		}
	}
	return out
}

func rolloutRestartPatch() string {
	// A unique annotation forces a new ReplicaSet rollout without changing behaviour.
	return fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"ntn.operators.dev/ha-e2e-restart":%q}}}}}`,
		time.Now().Format(time.RFC3339Nano))
}

// ---------------------------------------------------------------------------
// Durable OMM cache: outage-continuity across leader failover (acceptance test)
// ---------------------------------------------------------------------------

var satephGVR = schema.GroupVersionResource{Group: crdAPIGroup, Version: "v1alpha1", Resource: "satelliteephemeris"}

const (
	outageNS      = "default" // the mock Service DNS + --ephemeris-allowed-private-hosts are pinned to default
	mockName      = "celestrak-mock"
	mockFixtureCM = "celestrak-mock-fixture"
	satephName    = "oneweb-constellation"

	// Mirrors internal/controller propagationEpochLead / propagationRefreshInterval (the e2e package
	// cannot import an internal package). Only used for generous polling budgets, so mild drift is safe.
	haEpochLead        = 5 * time.Minute
	haPropagationCycle = 3 * time.Minute
)

// mockGPJSON is one fresh OMM (EPOCH=now, so within maxEpochAge for the whole run) served as /gp.json.
func mockGPJSON() string {
	epoch := time.Now().UTC().Format("2006-01-02T15:04:05.000000")
	return fmt.Sprintf(`[{"OBJECT_NAME":"ISS (ZARYA)","OBJECT_ID":"1998-067A","EPOCH":%q,`+
		`"MEAN_MOTION":15.49554387,"ECCENTRICITY":0.000588,"INCLINATION":51.6381,`+
		`"RA_OF_ASC_NODE":276.7884,"ARG_OF_PERICENTER":282.5765,"MEAN_ANOMALY":192.7824,`+
		`"EPHEMERIS_TYPE":0,"CLASSIFICATION_TYPE":"U","NORAD_CAT_ID":25544,`+
		`"ELEMENT_SET_NO":999,"REV_AT_EPOCH":47189,"BSTAR":0.00025892,`+
		`"MEAN_MOTION_DOT":0.00019394,"MEAN_MOTION_DDOT":0}]`, epoch)
}

// deployCelestrakMock creates the fixture ConfigMap + nginx Deployment + Service and waits for ready.
func (e *haEnv) deployCelestrakMock(t *testing.T) {
	t.Helper()
	labels := map[string]string{"app.kubernetes.io/name": mockName, "app.kubernetes.io/component": "e2e-fixture"}
	sel := map[string]string{"app.kubernetes.io/name": mockName}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: mockFixtureCM, Namespace: outageNS, Labels: labels},
		Data:       map[string]string{"gp.json": mockGPJSON()},
	}
	if _, err := e.cs.CoreV1().ConfigMaps(outageNS).Create(e.ctx, cm, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create mock fixture configmap: %v", err)
	}

	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: mockName, Namespace: outageNS, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: sel},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "nginx",
						Image: "nginx:1.27-alpine",
						Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 80}},
						VolumeMounts: []corev1.VolumeMount{{
							Name: "fixture", MountPath: "/usr/share/nginx/html/gp.json", SubPath: "gp.json", ReadOnly: true,
						}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/gp.json", Port: intstr.FromString("http")}},
							PeriodSeconds: 2, FailureThreshold: 15,
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "fixture",
						VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: mockFixtureCM},
							Items:                []corev1.KeyToPath{{Key: "gp.json", Path: "gp.json"}},
						}},
					}},
				},
			},
		},
	}
	if _, err := e.cs.AppsV1().Deployments(outageNS).Create(e.ctx, dep, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create mock deployment: %v", err)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: mockName, Namespace: outageNS, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: sel,
			Ports:    []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("http")}},
		},
	}
	if _, err := e.cs.CoreV1().Services(outageNS).Create(e.ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create mock service: %v", err)
	}

	eventually(t, 2*time.Minute, 3*time.Second, "celestrak-mock to become available", func() (bool, string) {
		d, err := e.cs.AppsV1().Deployments(outageNS).Get(e.ctx, mockName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return d.Status.AvailableReplicas >= 1, fmt.Sprintf("available=%d", d.Status.AvailableReplicas)
	})
}

// scaleMock sets the mock replica count (0 = simulate a total upstream outage).
func (e *haEnv) scaleMock(t *testing.T, replicas int32) {
	t.Helper()
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		d, err := e.cs.AppsV1().Deployments(outageNS).Get(e.ctx, mockName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		d.Spec.Replicas = &replicas
		_, err = e.cs.AppsV1().Deployments(outageNS).Update(e.ctx, d, metav1.UpdateOptions{})
		return err
	}); err != nil {
		t.Fatalf("scale mock to %d: %v", replicas, err)
	}
}

// createOutageSatEph creates a SatelliteEphemeris pointing at the in-cluster mock (no passPrediction —
// only the propagatedStates heartbeat matters here).
func (e *haEnv) createOutageSatEph(t *testing.T) {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": crdAPIGroup + "/v1alpha1",
		"kind":       "SatelliteEphemeris",
		"metadata":   map[string]any{"name": satephName, "namespace": outageNS},
		"spec": map[string]any{
			"source": map[string]any{
				"type":            "CelesTrak",
				"url":             "http://celestrak-mock.default.svc.cluster.local/gp.json",
				"refreshInterval": "4h",
			},
		},
	}}
	if _, err := e.dyn.Resource(satephGVR).Namespace(outageNS).Create(e.ctx, obj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create SatelliteEphemeris: %v", err)
	}
}

// satephEpoch returns status.propagatedStates[0].epochUnixMs, ok=false until it exists.
func (e *haEnv) satephEpoch() (int64, bool) {
	got, err := e.dyn.Resource(satephGVR).Namespace(outageNS).Get(e.ctx, satephName, metav1.GetOptions{})
	if err != nil {
		return 0, false
	}
	states, found, err := unstructured.NestedSlice(got.Object, "status", "propagatedStates")
	if err != nil || !found || len(states) == 0 {
		return 0, false
	}
	first, ok := states[0].(map[string]any)
	if !ok {
		return 0, false
	}
	epoch, found, err := unstructured.NestedInt64(first, "epochUnixMs")
	if err != nil || !found {
		return 0, false
	}
	return epoch, true
}

// waitEpochAdvances blocks until the propagated epoch strictly exceeds beyond, returning the new value.
func (e *haEnv) waitEpochAdvances(t *testing.T, beyond int64, timeout time.Duration, desc string) int64 {
	t.Helper()
	var latest int64
	eventually(t, timeout, 5*time.Second, desc, func() (bool, string) {
		ep, ok := e.satephEpoch()
		if !ok {
			return false, "no propagated epoch yet"
		}
		latest = ep
		return ep > beyond, fmt.Sprintf("epoch=%d beyond=%d", ep, beyond)
	})
	return latest
}

// cacheConfigMapDigest returns the durable cache ConfigMap's payload digest, ok=false if absent.
func (e *haEnv) cacheConfigMapDigest() (string, bool) {
	cm, err := e.cs.CoreV1().ConfigMaps(outageNS).Get(e.ctx, satephName+"-omm-cache", metav1.GetOptions{})
	if err != nil {
		return "", false
	}
	return cm.Annotations["ntn.operators.dev/omm-digest"], true
}

// TestHAOutageContinuityAcrossFailover is the MANDATED acceptance test for the durable OMM cache:
// warm the cache from the mock, force a TOTAL source outage, verify the epoch advances, KILL the
// current leader, keep the source down beyond propagationEpochLead, and verify the NEW leader —
// which starts with an EMPTY in-memory cache — restores the last-good OMMs from the durable
// ConfigMap and KEEPS advancing the epoch (staying in the future = runtime-push ready).
//
// This is the only test that proves the property the unit integration test cannot: REAL leader
// election + a REAL new process + REAL cross-process ConfigMap durability + real RBAC. Without the
// durable cache the post-failover leader would have nothing to propagate once the last pushed epoch
// expired, and the final assertions would fail.
//
// Precondition: the running chart must set --ephemeris-allowed-private-hosts to include
// celestrak-mock.default.svc.cluster.local, or the SSRF-safe client blocks the warm fetch (the test
// then fails fast at the warm step with that hint).
func TestHAOutageContinuityAcrossFailover(t *testing.T) {
	e := newHAEnv(t)
	e.waitReadyManagerPods(t)

	e.deployCelestrakMock(t)
	e.createOutageSatEph(t)
	t.Cleanup(func() {
		bg := context.Background()
		_ = e.dyn.Resource(satephGVR).Namespace(outageNS).Delete(bg, satephName, metav1.DeleteOptions{})
		_ = e.cs.AppsV1().Deployments(outageNS).Delete(bg, mockName, metav1.DeleteOptions{})
		_ = e.cs.CoreV1().Services(outageNS).Delete(bg, mockName, metav1.DeleteOptions{})
		_ = e.cs.CoreV1().ConfigMaps(outageNS).Delete(bg, mockFixtureCM, metav1.DeleteOptions{})
		// The <name>-omm-cache ConfigMap is owner-ref'd to the CR, so GC removes it with the CR.
	})

	// WARM: leader fetches, persists the durable cache, propagates.
	warm := e.waitEpochAdvances(t, 0, 4*time.Minute,
		"SatelliteEphemeris to warm — a stall here usually means the chart lacks "+
			"--ephemeris-allowed-private-hosts=celestrak-mock.default.svc.cluster.local")
	if _, ok := e.cacheConfigMapDigest(); !ok {
		t.Fatalf("durable cache ConfigMap %s-omm-cache absent after warm — persist not wired or RBAC-denied", satephName)
	}
	t.Logf("warm: epoch=%d, durable cache present", warm)

	// OUTAGE: mock to zero → every upstream fetch now fails.
	e.scaleMock(t, 0)
	eventually(t, time.Minute, 3*time.Second, "mock to report zero available replicas", func() (bool, string) {
		d, err := e.cs.AppsV1().Deployments(outageNS).Get(e.ctx, mockName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return d.Status.AvailableReplicas == 0, fmt.Sprintf("available=%d", d.Status.AvailableReplicas)
	})

	// PRE-FAILOVER: the current leader keeps advancing the epoch from its in-memory cache.
	preKill := e.waitEpochAdvances(t, warm, haPropagationCycle+3*time.Minute,
		"epoch to advance under outage before failover (leader serving in-memory cache)")
	t.Logf("pre-failover: epoch advanced to %d under outage", preKill)

	// KILL THE LEADER: the standby takes over with an EMPTY in-memory cache; source stays DOWN.
	holder0 := e.waitLeaseHolder(t)
	if h, err := e.leaseHolder(); err == nil && h != holder0 {
		holder0 = e.waitLeaseHolder(t)
	}
	leader0 := holderPodBase(holder0)
	killTime := time.Now()
	t.Logf("killing leader pod=%s while source is DOWN", leader0)
	if err := e.cs.CoreV1().Pods(e.ns).Delete(e.ctx, leader0, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete leader pod: %v", err)
	}
	eventually(t, 90*time.Second, time.Second, "lease to fail over to a new holder", func() (bool, string) {
		h, err := e.leaseHolder()
		if err != nil {
			return false, err.Error()
		}
		if h == "" || h == holder0 {
			return false, "holder still " + h
		}
		return true, ""
	})
	e.waitReadyManagerPods(t)
	newHolder, _ := e.leaseHolder()
	t.Logf("failed over to new leader holder=%s", newHolder)

	// THE PROOF: source STILL down, new leader started cold. Require ONE advance immediately (restore
	// worked) and then a FURTHER advance only after more than propagationEpochLead of wall time has
	// elapsed since the kill — by then the epoch pushed before the kill has expired, so the continued
	// advance can ONLY come from the new leader re-propagating the RESTORED durable cache. (The
	// window-expired → fetch-fails → fallback path cannot be reached in an e2e because minRefreshInterval
	// clamps the fetch window to 2h; it is covered deterministically by the unit integration test.)
	adv1 := e.waitEpochAdvances(t, preKill, 4*time.Minute,
		"first epoch advance after cold failover (durable-cache restore)")

	var adv2 int64
	eventually(t, haEpochLead+3*haPropagationCycle, 5*time.Second,
		"epoch to keep advancing until beyond the epoch-lead window after cold failover",
		func() (bool, string) {
			ep, ok := e.satephEpoch()
			if !ok {
				return false, "no propagated epoch"
			}
			adv2 = ep
			elapsed := time.Since(killTime)
			return ep > adv1 && elapsed >= haEpochLead,
				fmt.Sprintf("epoch=%d adv1=%d elapsedSinceKill=%s", ep, adv1, elapsed.Round(time.Second))
		})
	t.Logf("post-failover under sustained outage: epoch %d -> %d over %s", adv1, adv2, time.Since(killTime).Round(time.Second))

	// PUSH-READY: OCUDU ntn_config_update requires a FUTURE epoch, so a runtime push consumer always
	// has valid, non-expired ephemeris to send.
	if nowMs := time.Now().UnixMilli(); adv2 <= nowMs {
		t.Fatalf("post-failover epoch %d is not in the future (now=%d) — runtime push would have no valid ephemeris", adv2, nowMs)
	}
}
