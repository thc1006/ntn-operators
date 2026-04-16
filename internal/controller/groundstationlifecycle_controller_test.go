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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

var _ = Describe("GroundStationLifecycle Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-groundstation"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		groundstation := &ntnv1alpha1.GroundStationLifecycle{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind GroundStationLifecycle")
			err := k8sClient.Get(ctx, typeNamespacedName, groundstation)
			if err != nil && errors.IsNotFound(err) {
				resource := &ntnv1alpha1.GroundStationLifecycle{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: ntnv1alpha1.GroundStationLifecycleSpec{
						Hardware: ntnv1alpha1.HardwareSpec{
							Vendor: "ennoconn",
							Model:  "rugged-edge-5000",
						},
						Deployment: ntnv1alpha1.DeploymentSpec{
							Location: ntnv1alpha1.GeoLocation{
								Lat: "25.0330",
								Lon: "121.5654",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &ntnv1alpha1.GroundStationLifecycle{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance GroundStationLifecycle")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &GroundStationLifecycleReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
