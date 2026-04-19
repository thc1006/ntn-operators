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

package v1alpha1

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

func validNTNSlice() *ntnv1alpha1.NTNSlice {
	return &ntnv1alpha1.NTNSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: ntnv1alpha1.NTNSliceSpec{
			Tenant: "test-tenant",
			TerrestrialPath: ntnv1alpha1.PathSpec{
				Provider: "chunghwa-telecom",
				Priority: "primary",
			},
			SatellitePath: ntnv1alpha1.SatellitePathSpec{
				PathSpec:     ntnv1alpha1.PathSpec{Provider: "oneweb", Priority: "failover"},
				EphemerisRef: "sat-oneweb",
			},
			FailoverPolicy: ntnv1alpha1.FailoverPolicy{
				Triggers: []string{"rsrp < -120", "latency > 200"},
			},
		},
	}
}

func TestValidateCreate_ValidTriggers(t *testing.T) {
	v := &NTNSliceCustomValidator{}
	warnings, err := v.ValidateCreate(context.Background(), validNTNSlice())
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if len(warnings) > 0 {
		t.Errorf("expected no warnings, got: %v", warnings)
	}
}

func TestValidateCreate_InvalidTrigger(t *testing.T) {
	v := &NTNSliceCustomValidator{}
	ns := validNTNSlice()
	ns.Spec.FailoverPolicy.Triggers = []string{"rsrp << -120"}

	_, err := v.ValidateCreate(context.Background(), ns)
	if err == nil {
		t.Error("expected error for invalid trigger syntax")
	}
}

func TestValidateCreate_MixedTriggers(t *testing.T) {
	v := &NTNSliceCustomValidator{}
	ns := validNTNSlice()
	ns.Spec.FailoverPolicy.Triggers = []string{
		"rsrp < -120",
		"badmetric > 100",
	}

	_, err := v.ValidateCreate(context.Background(), ns)
	if err == nil {
		t.Error("expected error when any trigger is invalid")
	}
}

func TestValidateUpdate_ValidTriggers(t *testing.T) {
	v := &NTNSliceCustomValidator{}
	old := validNTNSlice()
	updated := validNTNSlice()
	updated.Spec.FailoverPolicy.Triggers = []string{"packetLoss > 5"}

	warnings, err := v.ValidateUpdate(context.Background(), old, updated)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if len(warnings) > 0 {
		t.Errorf("expected no warnings, got: %v", warnings)
	}
}

func TestValidateUpdate_InvalidTrigger(t *testing.T) {
	v := &NTNSliceCustomValidator{}
	old := validNTNSlice()
	updated := validNTNSlice()
	updated.Spec.FailoverPolicy.Triggers = []string{"rsrp -120"}

	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error for invalid trigger in update")
	}
}

func TestValidateDelete_AlwaysPasses(t *testing.T) {
	v := &NTNSliceCustomValidator{}
	warnings, err := v.ValidateDelete(context.Background(), validNTNSlice())
	if err != nil {
		t.Errorf("expected no error on delete, got: %v", err)
	}
	if len(warnings) > 0 {
		t.Errorf("expected no warnings, got: %v", warnings)
	}
}
