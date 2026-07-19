/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	ctrlrt "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/provider"
)

type runtimePushRecorder struct {
	provider.MockProvider
	pushes chan provider.RuntimeUpdate
}

func newRuntimePushRecorder() *runtimePushRecorder {
	return &runtimePushRecorder{pushes: make(chan provider.RuntimeUpdate, 8)}
}

func (p *runtimePushRecorder) PushRuntimeUpdate(
	_ context.Context, _ provider.ResolvedRemoteControl, update provider.RuntimeUpdate,
) error {
	p.pushes <- update
	return nil
}

var _ = Describe("NTNCellConfig runtime-push lifecycle (envtest)", func() {
	const (
		namespace = "default"
		cellName  = "runtime-push-envtest-cell"
		ephName   = "runtime-push-envtest-ephemeris"
	)
	cellKey := types.NamespacedName{Namespace: namespace, Name: cellName}
	ephKey := types.NamespacedName{Namespace: namespace, Name: ephName}

	It("re-pushes for an epoch-only freshness advance and after input currency catches up", func() {
		prov := newRuntimePushRecorder()
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  scheme.Scheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		Expect(err).NotTo(HaveOccurred())

		reconciler := &NTNCellConfigReconciler{
			Client:                  mgr.GetClient(),
			APIReader:               mgr.GetAPIReader(),
			Scheme:                  mgr.GetScheme(),
			Providers:               map[string]provider.NTNProvider{"ocudu": prov},
			MaxConcurrentReconciles: 1,
		}
		Expect(mgr.GetFieldIndexer().IndexField(context.Background(), &ntnv1alpha1.NTNCellConfig{},
			ephemerisRefIndexKey, indexNTNCellConfigByEphemerisRef)).To(Succeed())
		Expect(ctrl.NewControllerManagedBy(mgr).
			For(&ntnv1alpha1.NTNCellConfig{}, builder.WithPredicates(reconcileTriggerPredicate())).
			Watches(&ntnv1alpha1.SatelliteEphemeris{},
				handler.EnqueueRequestsFromMapFunc(reconciler.ephemerisToNTNCellConfig)).
			Named("ntncellconfig-runtime-push-envtest").
			WithOptions(ctrlrt.Options{MaxConcurrentReconciles: reconciler.MaxConcurrentReconciles}).
			Complete(reconciler)).To(Succeed())

		managerCtx, stopManager := context.WithCancel(context.Background())
		managerDone := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			managerDone <- mgr.Start(managerCtx)
		}()
		Expect(mgr.GetCache().WaitForCacheSync(managerCtx)).To(BeTrue())

		DeferCleanup(func() {
			stopManager()
			Eventually(managerDone, "10s").Should(Receive(BeNil()))

			cell := &ntnv1alpha1.NTNCellConfig{}
			if err := k8sClient.Get(context.Background(), cellKey, cell); err == nil {
				controllerutil.RemoveFinalizer(cell, "ntn.operators.dev/configmap-cleanup")
				Expect(k8sClient.Update(context.Background(), cell)).To(Succeed())
				Expect(k8sClient.Delete(context.Background(), cell)).To(Succeed())
			}
			eph := &ntnv1alpha1.SatelliteEphemeris{}
			if err := k8sClient.Get(context.Background(), ephKey, eph); err == nil {
				Expect(k8sClient.Delete(context.Background(), eph)).To(Succeed())
			}
		})

		eph := &ntnv1alpha1.SatelliteEphemeris{
			ObjectMeta: metav1.ObjectMeta{Name: ephName, Namespace: namespace},
			Spec: ntnv1alpha1.SatelliteEphemerisSpec{
				Source: ntnv1alpha1.EphemerisSource{
					Type:            "CelesTrak",
					URL:             "https://celestrak.org/runtime-push-envtest-v1",
					RefreshInterval: metav1.Duration{Duration: 4 * time.Hour},
				},
				Satellites: &ntnv1alpha1.SatelliteSelector{NoradIDs: []int{25544}},
			},
		}
		Expect(k8sClient.Create(context.Background(), eph)).To(Succeed())

		initialEpoch := time.Now().Add(time.Hour).UnixMilli()
		Expect(k8sClient.Get(context.Background(), ephKey, eph)).To(Succeed())
		eph.Status = ntnv1alpha1.SatelliteEphemerisStatus{
			LastUpdated:               &metav1.Time{Time: time.Now()},
			PropagatedStatesInputHash: propagationInputHash(eph.Spec),
			PropagatedStates: []ntnv1alpha1.PropagatedState{{
				Satellite:         "ISS",
				NoradID:           25544,
				EpochUnixMs:       initialEpoch,
				SourceEpochUnixMs: time.Now().Add(-time.Hour).UnixMilli(),
				ECEF: ntnv1alpha1.EphemerisECEF{
					PosX: 5_000_000,
					PosY: 4_000_000,
					PosZ: 3_000_000,
				},
			}},
		}
		Expect(k8sClient.Status().Update(context.Background(), eph)).To(Succeed())

		cell := ccWithRemoteControl()
		cell.Name = cellName
		cell.Namespace = namespace
		cell.Spec.EphemerisRef = ephName
		Expect(k8sClient.Create(context.Background(), cell)).To(Succeed())

		var firstPush provider.RuntimeUpdate
		Eventually(prov.pushes, "15s").Should(Receive(&firstPush))
		Expect(firstPush.EpochUnixMs).To(Equal(initialEpoch))

		var settledMarker string
		Eventually(func(g Gomega) {
			current := &ntnv1alpha1.NTNCellConfig{}
			g.Expect(k8sClient.Get(context.Background(), cellKey, current)).To(Succeed())
			condition := meta.FindStatusCondition(current.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed)
			g.Expect(condition).NotTo(BeNil())
			g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(condition.Reason).To(Equal("Pushed"))
			settledMarker = condition.Message
		}, "10s", "100ms").Should(Succeed())

		// A status-only event with the same generation and propagated epoch must cross
		// the informer watch but deduplicate at the persisted runtime marker.
		Expect(k8sClient.Get(context.Background(), ephKey, eph)).To(Succeed())
		eph.Status.LastUpdated = &metav1.Time{Time: time.Now().Add(time.Second)}
		Expect(k8sClient.Status().Update(context.Background(), eph)).To(Succeed())
		Consistently(prov.pushes, "2s", "100ms").ShouldNot(Receive())

		currentCell := &ntnv1alpha1.NTNCellConfig{}
		Expect(k8sClient.Get(context.Background(), cellKey, currentCell)).To(Succeed())
		condition := meta.FindStatusCondition(currentCell.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Message).To(Equal(settledMarker))

		// Advance only the propagated epoch while the spec, generation, and input
		// hash stay fixed. This is the between-GP-fetch freshness path: the watch
		// must fan out exactly one runtime update for the new epoch.
		Expect(k8sClient.Get(context.Background(), ephKey, eph)).To(Succeed())
		epochOnlyGeneration := eph.Generation
		epochOnlySpec := eph.Spec.DeepCopy()
		epochOnlyInputHash := eph.Status.PropagatedStatesInputHash
		epochOnlyEpoch := initialEpoch + (3 * time.Minute).Milliseconds()
		eph.Status.PropagatedStates[0].EpochUnixMs = epochOnlyEpoch
		Expect(k8sClient.Status().Update(context.Background(), eph)).To(Succeed())

		var epochOnlyPush provider.RuntimeUpdate
		Eventually(prov.pushes, "10s").Should(Receive(&epochOnlyPush))
		Expect(epochOnlyPush.EpochUnixMs).To(Equal(epochOnlyEpoch))

		expectedEpochOnlyMarker := fmt.Sprintf(
			"ephemerisRef=%s ephGeneration=%d norad=%d epoch=%d",
			ephName, epochOnlyGeneration, 25544, epochOnlyEpoch,
		)
		Eventually(func(g Gomega) {
			currentEph := &ntnv1alpha1.SatelliteEphemeris{}
			g.Expect(k8sClient.Get(context.Background(), ephKey, currentEph)).To(Succeed())
			g.Expect(currentEph.Spec).To(Equal(*epochOnlySpec))
			g.Expect(currentEph.Generation).To(Equal(epochOnlyGeneration))
			g.Expect(currentEph.Status.PropagatedStatesInputHash).To(Equal(epochOnlyInputHash))

			current := &ntnv1alpha1.NTNCellConfig{}
			g.Expect(k8sClient.Get(context.Background(), cellKey, current)).To(Succeed())
			fresh := meta.FindStatusCondition(current.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed)
			g.Expect(fresh).NotTo(BeNil())
			g.Expect(fresh.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(fresh.Reason).To(Equal("Pushed"))
			g.Expect(fresh.Message).To(Equal(expectedEpochOnlyMarker))
		}, "10s", "100ms").Should(Succeed())
		Consistently(prov.pushes, "2s", "100ms").ShouldNot(Receive())

		// The apiserver owns generation: changing a propagation input bumps it while
		// the status subresource still describes the old input set. The cached
		// controller must fail closed and settle without pushing stale data.
		Expect(k8sClient.Get(context.Background(), ephKey, eph)).To(Succeed())
		oldGeneration := eph.Generation
		eph.Spec.Source.URL = "https://celestrak.org/runtime-push-envtest-v2"
		Expect(k8sClient.Update(context.Background(), eph)).To(Succeed())

		Eventually(func(g Gomega) {
			currentEph := &ntnv1alpha1.SatelliteEphemeris{}
			g.Expect(k8sClient.Get(context.Background(), ephKey, currentEph)).To(Succeed())
			g.Expect(currentEph.Generation).To(Equal(oldGeneration + 1))

			current := &ntnv1alpha1.NTNCellConfig{}
			g.Expect(k8sClient.Get(context.Background(), cellKey, current)).To(Succeed())
			stale := meta.FindStatusCondition(current.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed)
			g.Expect(stale).NotTo(BeNil())
			g.Expect(stale.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(stale.Reason).To(Equal(ephemerisReasonInputsStale))
		}, "10s", "100ms").Should(Succeed())
		Consistently(prov.pushes, "1s", "100ms").ShouldNot(Receive())

		// Once the producer status catches up to the new inputs and epoch, the status
		// watch fans out exactly one fresh runtime push. The controller's own status
		// write must not create a hot requeue or a fourth push.
		Expect(k8sClient.Get(context.Background(), ephKey, eph)).To(Succeed())
		nextGeneration := eph.Generation
		nextEpoch := epochOnlyEpoch + (3 * time.Minute).Milliseconds()
		eph.Status.PropagatedStatesInputHash = propagationInputHash(eph.Spec)
		eph.Status.PropagatedStates[0].EpochUnixMs = nextEpoch
		Expect(k8sClient.Status().Update(context.Background(), eph)).To(Succeed())

		var secondPush provider.RuntimeUpdate
		Eventually(prov.pushes, "10s").Should(Receive(&secondPush))
		Expect(secondPush.EpochUnixMs).To(Equal(nextEpoch))

		Eventually(func(g Gomega) {
			current := &ntnv1alpha1.NTNCellConfig{}
			g.Expect(k8sClient.Get(context.Background(), cellKey, current)).To(Succeed())
			fresh := meta.FindStatusCondition(current.Status.Conditions, ntnv1alpha1.ConditionEphemerisPushed)
			g.Expect(fresh).NotTo(BeNil())
			g.Expect(fresh.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(fresh.Reason).To(Equal("Pushed"))
			g.Expect(fresh.Message).To(Equal(fmt.Sprintf(
				"ephemerisRef=%s ephGeneration=%d norad=%d epoch=%d",
				ephName, nextGeneration, 25544, nextEpoch,
			)))
		}, "10s", "100ms").Should(Succeed())
		Consistently(prov.pushes, "2s", "100ms").ShouldNot(Receive())
	})
})
