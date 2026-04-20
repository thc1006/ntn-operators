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
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/slice"
)

// NTNSliceCustomValidator validates NTNSlice admission requests.
// It checks failoverPolicy.triggers[] syntax at admission time,
// catching errors that CEL/OpenAPI schema validation cannot express.
type NTNSliceCustomValidator struct{}

// ValidateCreate validates trigger syntax on creation.
func (v *NTNSliceCustomValidator) ValidateCreate(
	_ context.Context, ns *ntnv1alpha1.NTNSlice,
) (admission.Warnings, error) {
	return nil, validateTriggers(ns)
}

// ValidateUpdate validates trigger syntax on update.
func (v *NTNSliceCustomValidator) ValidateUpdate(
	_ context.Context, _, ns *ntnv1alpha1.NTNSlice,
) (admission.Warnings, error) {
	return nil, validateTriggers(ns)
}

// ValidateDelete allows all deletions.
func (v *NTNSliceCustomValidator) ValidateDelete(
	_ context.Context, _ *ntnv1alpha1.NTNSlice,
) (admission.Warnings, error) {
	return nil, nil
}

// validateTriggers parses each failoverPolicy.triggers entry and
// returns an error listing all invalid trigger expressions.
func validateTriggers(ns *ntnv1alpha1.NTNSlice) error {
	var errs []string
	for i, t := range ns.Spec.FailoverPolicy.Triggers {
		if _, err := slice.ParseTrigger(t); err != nil {
			errs = append(errs, fmt.Sprintf(
				"triggers[%d] %q: %v", i, t, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid failoverPolicy.triggers: %v", errs)
	}
	return nil
}
