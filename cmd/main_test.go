/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"
)

// TestNewManagerOptionsAreSafe pins that the safe-shutdown configuration reaches the SAME manager Options
// main() runs with — not merely that a constant is negative. It calls the exact constructor main() uses;
// deleting that call in main() fails to compile (the returned Options feed ctrl.NewManager). A nil
// GracefulShutdownTimeout would revert controller-runtime to a FINITE 30s default, and with a finite
// timeout runnableGroup.StopAndWait returns on ctx-expiry while a reconcile is still running, so the
// deferred lease release hands the lease to a standby while the old leader is still doing lease-guarded
// work (controller-runtime #1132).
func TestNewManagerOptionsAreSafe(t *testing.T) {
	o := newManagerOptions(nil, metricsserver.Options{}, ":8081", true)

	if !o.LeaderElection {
		t.Fatal("LeaderElection must be wired through from the constructor argument")
	}
	if !o.LeaderElectionReleaseOnCancel {
		t.Fatal("LeaderElectionReleaseOnCancel must be true for fast active-passive failover")
	}
	if o.GracefulShutdownTimeout == nil {
		t.Fatal("GracefulShutdownTimeout must be wired: a nil option reverts controller-runtime to a finite 30s default")
	}
	if *o.GracefulShutdownTimeout >= 0 {
		t.Fatalf("GracefulShutdownTimeout must be NEGATIVE (wait indefinitely) so the lease is released only "+
			"after runnables stop; got %s — a finite timeout risks a split-brain release", *o.GracefulShutdownTimeout)
	}
	if o.RenewDeadline == nil || *o.RenewDeadline != leaderRenewDeadline {
		t.Fatalf("RenewDeadline must be set explicitly to %s (the value the shutdown-headroom check reasons about), got %v",
			leaderRenewDeadline, o.RenewDeadline)
	}
	if o.LeaseDuration == nil || *o.LeaseDuration != leaderLeaseDuration {
		t.Fatalf("LeaseDuration must be set explicitly to %s, got %v", leaderLeaseDuration, o.LeaseDuration)
	}
	if o.RetryPeriod == nil || *o.RetryPeriod != leaderRetryPeriod {
		t.Fatalf("RetryPeriod must be set explicitly to %s, got %v", leaderRetryPeriod, o.RetryPeriod)
	}
}

// TestManifestsGiveShutdownHeadroom checks each shipped deployment manifest gives the leader-lease release
// operational HEADROOM: terminationGracePeriodSeconds >= RenewDeadline. This is NOT a safety guarantee —
// the release only starts after the runnables stop, so if little grace remains the kubelet SIGKILL falls
// back safely to lease-expiry failover (the safety property is the negative GracefulShutdownTimeout,
// pinned by TestNewManagerOptionsAreSafe). Manifests are parsed as real PodSpecs (not regex) and
// include the OLM bundle CSV — a third deployment source `make bundle` does not regenerate from
// config/manager. The Helm chart renders through templating, so its grace period + the replicas>1 PDB
// gate are verified from rendered output in CI (.github/workflows/test-chart.yml), not here.
func TestManifestsGiveShutdownHeadroom(t *testing.T) {
	checkGrace(t, "../config/manager/manager.yaml", graceFromDeploymentYAML)
	checkGrace(t, "../bundle/manifests/ntn-operators.clusterserviceversion.yaml", graceFromCSV)
}

func checkGrace(t *testing.T, path string, extract func([]byte) (*int64, error)) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	tg, err := extract(b)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if tg == nil {
		t.Fatalf("%s: manager Deployment has no terminationGracePeriodSeconds — the shutdown headroom check is unguarded",
			path)
	}
	if grace := time.Duration(*tg) * time.Second; grace < leaderRenewDeadline {
		t.Fatalf("%s: terminationGracePeriodSeconds (%s) must be >= RenewDeadline (%s) to give the lease "+
			"release headroom when runnables stop promptly", path, grace, leaderRenewDeadline)
	}
}

// graceFromDeploymentYAML finds the Deployment in a (possibly multi-doc) manifest and returns its
// PodSpec.TerminationGracePeriodSeconds.
func graceFromDeploymentYAML(b []byte) (*int64, error) {
	for doc := range strings.SplitSeq(string(b), "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var tm metav1.TypeMeta
		if err := yaml.Unmarshal([]byte(doc), &tm); err != nil {
			continue // not a typed k8s object; skip
		}
		if tm.Kind != "Deployment" {
			continue
		}
		var dep appsv1.Deployment
		if err := yaml.Unmarshal([]byte(doc), &dep); err != nil {
			return nil, fmt.Errorf("Deployment document did not decode: %w", err) // fail closed, don't skip
		}
		return dep.Spec.Template.Spec.TerminationGracePeriodSeconds, nil
	}
	return nil, fmt.Errorf("no Deployment document found")
}

// graceFromCSV walks the ClusterServiceVersion install strategy to the manager Deployment's PodSpec.
func graceFromCSV(b []byte) (*int64, error) {
	var csv struct {
		Spec struct {
			Install struct {
				Spec struct {
					Deployments []struct {
						Name string                `json:"name"`
						Spec appsv1.DeploymentSpec `json:"spec"`
					} `json:"deployments"`
				} `json:"spec"`
			} `json:"install"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(b, &csv); err != nil {
		return nil, err
	}
	// Match the manager Deployment by name (unique) rather than by position, so a future second
	// install deployment cannot silently shift which PodSpec is checked.
	var grace *int64
	var found int
	for _, d := range csv.Spec.Install.Spec.Deployments {
		if strings.Contains(d.Name, "controller-manager") {
			grace = d.Spec.Template.Spec.TerminationGracePeriodSeconds
			found++
		}
	}
	if found != 1 {
		return nil, fmt.Errorf("expected exactly one controller-manager deployment in the CSV install strategy, got %d",
			found)
	}
	return grace, nil
}

// TestCacheSyncReadyCheck pins that /readyz fails until caches sync and passes after.
// Mutation guard: inverting the `ready.Load()` branch fails one of the two assertions.
func TestCacheSyncReadyCheck(t *testing.T) {
	var ready atomic.Bool
	check := cacheSyncReadyCheck(&ready)

	if err := check(nil); err == nil {
		t.Fatal("readyz must FAIL before informer caches sync (ready=false)")
	}
	ready.Store(true)
	if err := check(nil); err != nil {
		t.Fatalf("readyz must PASS once caches sync (ready=true), got %v", err)
	}
}

// TestCacheReadyRunnable_NeedLeaderElectionFalse is the load-bearing contract: if this
// runnable needed leader election it would run only on the elected leader, so a standby's
// readyz would never pass and rollouts would deadlock. It MUST be a non-leader runnable.
func TestCacheReadyRunnable_NeedLeaderElectionFalse(t *testing.T) {
	if (cacheReadyRunnable{}).NeedLeaderElection() {
		t.Fatal("cacheReadyRunnable.NeedLeaderElection() must be false (non-leader runnable) " +
			"or the standby never becomes Ready and rollouts deadlock")
	}
}

// TestCacheReadyRunnable_StartSetsFlagThenBlocks proves Start flips the flag (so readyz
// can pass) and then blocks until context cancel (so it does not exit and get restarted).
func TestCacheReadyRunnable_StartSetsFlagThenBlocks(t *testing.T) {
	var ready atomic.Bool
	r := cacheReadyRunnable{ready: &ready}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	// Flag must be set essentially immediately (before Start blocks on ctx.Done).
	deadline := time.Now().Add(2 * time.Second)
	for !ready.Load() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !ready.Load() {
		t.Fatal("Start must set ready=true before blocking on ctx.Done")
	}

	// It must still be running (blocked) — done should not have fired yet.
	select {
	case err := <-done:
		t.Fatalf("Start returned before ctx cancel (err=%v); it must block", err)
	default:
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned %v after ctx cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
}
