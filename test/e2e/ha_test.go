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

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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
		t.Skip("HA suite is DESTRUCTIVE (cordons every node, evicts manager pods, mutates cluster RBAC); " +
			"set HA_ALLOW_DESTRUCTIVE=1 and point KUBECONFIG at a disposable Kind cluster to run it")
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
	return time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second
}

// holderPodBase maps a controller-runtime holderIdentity ("<pod>_<uuid>") to its owning pod name.
func holderPodBase(holder string) string {
	if i := strings.LastIndex(holder, "_"); i > 0 {
		return holder[:i]
	}
	return holder
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
	holder0 := e.waitLeaseHolder(t)
	leader0 := holderPodBase(holder0)
	t.Logf("initial leader pod=%s holder=%s", leader0, holder0)

	if err := e.cs.CoreV1().Pods(e.ns).Delete(e.ctx, leader0, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete leader pod: %v", err)
	}
	start := time.Now()
	eventually(t, time.Minute, time.Second, "the lease to fail over to a new holder", func() (bool, string) {
		h, err := e.leaseHolder()
		if err != nil {
			return false, err.Error()
		}
		if h == "" || holderPodBase(h) == leader0 {
			return false, "holder still " + h
		}
		return true, ""
	})
	h1, _ := e.leaseHolder()
	t.Logf("GRACEFUL failover took %s; new holder=%s", time.Since(start).Round(time.Millisecond), h1)
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
		if len(pod.Status.ContainerStatuses) == 0 || pod.Status.ContainerStatuses[0].ContainerID == "" {
			t.Logf("attempt %d: leader pod %s has no running container id — re-resolving", attempt, leader0)
			continue
		}
		node := pod.Spec.NodeName
		cid := strings.TrimPrefix(pod.Status.ContainerStatuses[0].ContainerID, "containerd://")
		// Re-confirm this is STILL the leader immediately before the kill; if it moved during resolution, retry.
		if h, _ := e.leaseHolder(); h != holder0 {
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
	// currentHealthy accounting deterministic. Uncordon on cleanup.
	nodes, err := e.cs.CoreV1().Nodes().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	// Register the uncordon cleanup BEFORE cordoning, so a mid-loop cordon failure (Fatal) still releases the
	// nodes already cordoned. A cordon that SILENTLY failed would let the replacement become Ready and the
	// second eviction be legitimately allowed — which we would then misreport as "PDB unprotected" — so a
	// SETUP cordon failure is Fatal; the cleanup uncordon is best-effort but reported.
	t.Cleanup(func() {
		var stuck []string
		for i := range nodes.Items {
			if err := e.setCordon(nodes.Items[i].Name, false); err != nil {
				stuck = append(stuck, nodes.Items[i].Name)
				t.Logf("cleanup: uncordon %s failed: %v", nodes.Items[i].Name, err)
			}
		}
		if len(stuck) > 0 {
			t.Errorf("CRITICAL: cleanup left node(s) cordoned %v — the cluster may not schedule new pods", stuck)
		}
	})
	for i := range nodes.Items {
		if err := e.setCordon(nodes.Items[i].Name, true); err != nil {
			t.Fatalf("setup: cordon node %s failed: %v", nodes.Items[i].Name, err)
		}
	}

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
		case apierrors.IsTooManyRequests(err) || strings.Contains(err.Error(), "disruption budget"):
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

	// Invariant over the window: a NEW-generation pod (a pod-template-hash absent before the rollout) whose
	// container is RUNNING but which never becomes Ready — i.e. held by the cache-sync /readyz gate, not a
	// pull/schedule failure — appears, while a Ready pod is preserved throughout. The structural guarantee is
	// maxUnavailable=0 (asserted by the chart topology check), so kube cannot remove a healthy old pod before a
	// new one is Ready; this loop is the runtime confirmation. Polled at 1s — the "never below 1 Ready" claim
	// holds to that sampling resolution.
	sawGatedNewPod := false
	sawNewPod := false
	minReady := 1 << 30
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if pods, err := e.managerPods(); err == nil {
			ready, gatedNew, sawNew := scanRolloutGate(pods, oldHashes)
			if sawNew {
				sawNewPod = true
			}
			if ready < minReady {
				minReady = ready
			}
			if gatedNew && ready >= 1 {
				sawGatedNewPod = true
			}
		}
		time.Sleep(time.Second)
	}
	if !sawGatedNewPod {
		t.Fatalf("did not observe a RUNNING new-generation pod held NotReady by the cache-sync gate while a "+
			"Ready pod was preserved (sawNewPod=%v, minReady=%d) — if no new pod appeared the rollout may not "+
			"have progressed; if it came up Ready the gate failed to hold it", sawNewPod, minReady)
	}
	if minReady < 1 {
		t.Fatalf("a healthy pod was torn down under the broken rollout (min ready=%d, 1s resolution) — the "+
			"cache-sync gate / maxUnavailable=0 guarantee failed", minReady)
	}
	t.Logf("cache-sync /readyz gate held: a RUNNING but NotReady new-generation pod was gated while ready pods "+
		"stayed >= %d over the window", minReady)

	// Restore RBAC + roll again, and confirm full recovery (the Cleanup is the failsafe).
	if err := restore(); err != nil {
		t.Fatalf("restore RBAC + roll: %v", err)
	}
	e.waitReadyManagerPods(t)
	t.Log("recovered: 2 ready manager pods after restoring RBAC")
}

// scanRolloutGate summarizes one poll of the manager pods during a rollout: ready = count of Ready pods;
// gatedNew = a NEW-generation pod (pod-template-hash absent from oldHashes) is RUNNING but held NotReady (the
// cache-sync gate, not a pull/schedule failure); sawNew = any new-generation pod exists at all. Extracted from
// TestHACacheSyncRolloutGate to keep that test's cyclomatic complexity bounded.
func scanRolloutGate(pods []corev1.Pod, oldHashes map[string]bool) (ready int, gatedNew, sawNew bool) {
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil {
			continue
		}
		isNew := false
		if h := p.Labels["pod-template-hash"]; h != "" && !oldHashes[h] {
			isNew, sawNew = true, true
		}
		switch {
		case podReady(p):
			ready++
		case isNew && containerRunning(p):
			gatedNew = true // started (not a pull/schedule failure) yet held NotReady by the gate
		}
	}
	return ready, gatedNew, sawNew
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
