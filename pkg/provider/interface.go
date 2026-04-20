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

package provider

import (
	"context"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// EphemerisUpdate carries fresh ephemeris data for a live update push.
// The provider uses whichever representation is populated (ECEF or orbital).
type EphemerisUpdate struct {
	// ECEF state vector (mutually exclusive with Orbital).
	ECEF *ntnv1alpha1.EphemerisECEF
	// Keplerian orbital elements (mutually exclusive with ECEF).
	Orbital *ntnv1alpha1.EphemerisOrbital
}

// NTNProvider abstracts NTN backend interactions for cell configuration
// and ephemeris updates. Ground station lifecycle is handled directly
// by its respective controller.
//
// All current providers (OCUDU, future OAI) store configuration in a
// ConfigMap. If a future provider uses a different artifact type,
// the interface and controller should be generalized at that time.
type NTNProvider interface {
	// ApplyCellConfig applies NTN radio parameters to the backend.
	// crName is the NTNCellConfig CR name (used to scope the provider artifact).
	ApplyCellConfig(ctx context.Context, crName string, spec *ntnv1alpha1.NTNCellConfigSpec) error

	// GetCellStatus returns the current applied configuration status.
	GetCellStatus(ctx context.Context, crName, namespace string) (*ntnv1alpha1.NTNCellConfigStatus, error)

	// PushEphemerisUpdate pushes fresh ephemeris data to the backend.
	PushEphemerisUpdate(ctx context.Context, crName, namespace string, update EphemerisUpdate) error

	// ConfigMapName returns the name of the ConfigMap managed by this
	// provider for the given CR name. Used by the controller for
	// OwnerReference and finalizer cleanup.
	//
	// Note: All current and planned providers use ConfigMap as the
	// artifact type. If a non-ConfigMap provider is added, this method
	// should be generalized to return a typed artifact reference.
	ConfigMapName(crName string) string
}
