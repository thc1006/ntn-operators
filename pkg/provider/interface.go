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

// NTNProvider abstracts NTN backend interactions for cell configuration.
// Ephemeris and ground station lifecycle are handled directly by their
// respective controllers (not via Provider).
//
// Implementations:
//   - OCUDU: generates geo_ntn.yml and writes to a ConfigMap (future: Helm values overlay / O1 NETCONF)
//   - OAI: generates OAI NTN config via oai-operators Helm (planned)
//   - Aalyria: applies config via Spacetime gRPC API, pin v21.0 (planned)
type NTNProvider interface {
	// ApplyCellConfig applies NTN radio parameters to the backend.
	// Returns an error if the configuration could not be applied.
	ApplyCellConfig(ctx context.Context, spec *ntnv1alpha1.NTNCellConfigSpec) error

	// GetCellStatus returns the current applied configuration status
	// for the given namespace.
	GetCellStatus(ctx context.Context, namespace string) (*ntnv1alpha1.NTNCellConfigStatus, error)
}
