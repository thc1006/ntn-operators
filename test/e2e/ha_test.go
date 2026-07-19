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
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	// haLeaseName must equal LeaderElectionID in cmd/main.go.
	haLeaseName = "b1076767.operators.dev"
	// haLeaseDuration must track leaderLeaseDuration in cmd/main.go — an ungraceful (SIGKILL) failover
	// waits out at most this before the standby can steal the lease.
	haLeaseDuration = 15 * time.Second
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
	return &haEnv{cs: cs, dyn: dyn, ns: haNamespace(), ctx: context.Background()}
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
	holder0 := e.waitLeaseHolder(t)
	leader0 := holderPodBase(holder0)

	pod, err := e.cs.CoreV1().Pods(e.ns).Get(e.ctx, leader0, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get leader pod: %v", err)
	}
	node := pod.Spec.NodeName
	if len(pod.Status.ContainerStatuses) == 0 || pod.Status.ContainerStatuses[0].ContainerID == "" {
		t.Fatalf("leader pod %s has no running container id", leader0)
	}
	cid := strings.TrimPrefix(pod.Status.ContainerStatuses[0].ContainerID, "containerd://")
	t.Logf("initial leader pod=%s node=%s container=%.12s", leader0, node, cid)

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available: the ungraceful (node-level SIGKILL) test needs a Kind cluster")
	}
	// Leadership can legitimately move in the gap while we resolved the pod/container id above; if it has,
	// the container we're about to kill is no longer the leader and the RTO would be meaningless — skip.
	if h, _ := e.leaseHolder(); h != holder0 {
		t.Skipf("leadership moved before the kill (%s -> %s); nothing to measure", holder0, h)
	}
	// crictl stop --timeout 0 SIGKILLs the container immediately — no SIGTERM, so no lease release.
	kill := exec.Command("docker", "exec", node, "crictl", "stop", "--timeout", "0", cid)
	if out, err := kill.CombinedOutput(); err != nil {
		t.Skipf("could not SIGKILL the container via docker/crictl (non-Kind cluster?): %v: %s", err, out)
	}
	start := time.Now() // measure the RTO from the fault, NOT including the docker-exec setup
	eventually(t, haLeaseDuration+30*time.Second, time.Second, "ungraceful failover", func() (bool, string) {
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
	t.Logf("ungraceful (SIGKILL) failover RTO=%s (LeaseDuration=%s)", rto.Round(time.Millisecond), haLeaseDuration)

	// The lease renews every few seconds, so the standby cannot steal it until ~LeaseDuration after the last
	// renew. A graceful release would be ~1 s; require the RTO to be clearly in the lease-timeout regime,
	// proving no release happened, while bounding the top for a slow CI.
	if rto < 8*time.Second {
		t.Fatalf("ungraceful RTO %s too fast — the lease was released, not the LeaseDuration path", rto)
	}
	// Upper bound kept below the eventually window (LeaseDuration+30s) so a legitimately-slow-but-bounded
	// failover trips THIS clear message rather than the generic eventually timeout.
	if rto > haLeaseDuration+20*time.Second {
		t.Fatalf("ungraceful failover RTO %s exceeded LeaseDuration+margin", rto)
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
	for i := range nodes.Items {
		e.setCordon(t, nodes.Items[i].Name, true)
	}
	t.Cleanup(func() {
		for i := range nodes.Items {
			e.setCordon(t, nodes.Items[i].Name, false)
		}
	})

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

func (e *haEnv) setCordon(t *testing.T, node string, unschedulable bool) {
	t.Helper()
	patch := fmt.Sprintf(`{"spec":{"unschedulable":%v}}`, unschedulable)
	_, err := e.cs.CoreV1().Nodes().Patch(e.ctx, node, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		t.Logf("warning: patch node %s unschedulable=%v: %v", node, unschedulable, err)
	}
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
	deploy := e.deployName(t)
	// Idempotent: the explicit in-body restore (to assert recovery) and the t.Cleanup failsafe both call this,
	// but sync.Once collapses them to a single RBAC-restore + rollout — no redundant second rollout, and the
	// Cleanup still restores if the test fails before the in-body call.
	var restoreOnce sync.Once
	restore := func() {
		restoreOnce.Do(func() {
			if cur, err := e.cs.RbacV1().ClusterRoles().Get(context.Background(), crName, metav1.GetOptions{}); err == nil {
				cur.Rules = orig.Rules
				_, _ = e.cs.RbacV1().ClusterRoles().Update(context.Background(), cur, metav1.UpdateOptions{})
			}
			_, _ = e.cs.AppsV1().Deployments(e.ns).Patch(context.Background(), deploy,
				types.StrategicMergePatchType, []byte(rolloutRestartPatch()), metav1.PatchOptions{})
		})
	}
	t.Cleanup(restore)

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

	// Invariant over the window: a NotReady new pod appears (the broken replica the cache-sync /readyz gate
	// holds back) AND the count of Ready manager pods never drops below 1 (the healthy version is never torn
	// down under a broken new one). maxUnavailable rounds to 0 for 2 replicas, so old stays Ready while the
	// surged new pod is stuck NotReady.
	sawGatedNewPod := false
	minReady := 1 << 30
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		ready, notReady, err := e.countReady()
		if err == nil {
			if ready < minReady {
				minReady = ready
			}
			if notReady >= 1 && ready >= 1 {
				sawGatedNewPod = true
			}
		}
		time.Sleep(3 * time.Second)
	}
	if !sawGatedNewPod {
		t.Fatal("expected a NotReady new pod (cache-sync gate holding a broken replica) while a Ready pod is preserved")
	}
	if minReady < 1 {
		t.Fatalf("healthy version torn down under a broken rollout (min ready %d) — cache-sync gate failed", minReady)
	}
	t.Logf("cache-sync /readyz gate held: a NotReady new pod was gated while ready pods stayed >= %d", minReady)

	// Restore RBAC + roll again, and confirm full recovery (also guaranteed by Cleanup).
	restore()
	e.waitReadyManagerPods(t)
	t.Log("recovered: 2 ready manager pods after restoring RBAC")
}

func (e *haEnv) deployName(t *testing.T) string {
	t.Helper()
	dl, err := e.cs.AppsV1().Deployments(e.ns).List(e.ctx, metav1.ListOptions{LabelSelector: managerSelector})
	if err != nil || len(dl.Items) == 0 {
		t.Fatalf("find manager Deployment: err=%v n=%d", err, len(dl.Items))
	}
	return dl.Items[0].Name
}

func (e *haEnv) managerClusterRoleName(t *testing.T) string {
	t.Helper()
	crl, err := e.cs.RbacV1().ClusterRoles().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list ClusterRoles: %v", err)
	}
	for i := range crl.Items {
		cr := &crl.Items[i]
		if !strings.Contains(cr.Name, "manager-role") {
			continue
		}
		for j := range cr.Rules {
			if ruleHasGroup(&cr.Rules[j], crdAPIGroup) {
				return cr.Name
			}
		}
	}
	t.Fatalf("could not find the manager ClusterRole with %s rules", crdAPIGroup)
	return ""
}

func ruleHasGroup(r *rbacv1.PolicyRule, group string) bool {
	for _, g := range r.APIGroups {
		if g == group || g == "*" {
			return true
		}
	}
	return false
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
