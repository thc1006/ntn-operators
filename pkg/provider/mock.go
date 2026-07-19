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
	"sync"

	"k8s.io/apimachinery/pkg/runtime"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// Compile-time check that MockProvider implements NTNProvider.
var _ NTNProvider = &MockProvider{}

// MockProvider is a test double for NTNProvider.
type MockProvider struct {
	mu             sync.Mutex
	ApplyErr       error
	StatusErr      error
	EphemerisErr   error
	ApplyCalls     int
	StatusCalls    int
	EphemerisCalls int
	RuntimeErr     error
	RuntimeCalls   int
	CleanupErr     error
	CleanupCalls   int
	LastSpec       *ntnv1alpha1.NTNCellConfigSpec
	LastEphemeris  *EphemerisUpdate
	LastRuntime    *RuntimeUpdate
	LastTarget     *ResolvedRemoteControl
	StatusValue    *ntnv1alpha1.NTNCellConfigStatus
	// RuntimeHistory / TargetHistory record a DEEP COPY of every PushRuntimeUpdate call's frame (not just the
	// last pointer), so a test can compare successive pushes byte-for-byte — e.g. proving a crash re-push
	// re-emits an identical same-epoch payload rather than a mutated/aliased view of the latest one.
	RuntimeHistory []RuntimeUpdate
	TargetHistory  []ResolvedRemoteControl
}

func (m *MockProvider) ApplyCellConfig(
	_ context.Context, _ *ntnv1alpha1.NTNCellConfig, spec *ntnv1alpha1.NTNCellConfigSpec, _ *runtime.Scheme,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ApplyCalls++
	m.LastSpec = spec
	return m.ApplyErr
}

func (m *MockProvider) GetCellStatus(_ context.Context, _, _ string) (*ntnv1alpha1.NTNCellConfigStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.StatusCalls++
	if m.StatusValue != nil {
		return m.StatusValue, m.StatusErr
	}
	return &ntnv1alpha1.NTNCellConfigStatus{}, m.StatusErr
}

func (m *MockProvider) PushEphemerisUpdate(
	_ context.Context, _ *ntnv1alpha1.NTNCellConfig, update EphemerisUpdate,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.EphemerisCalls++
	m.LastEphemeris = &update
	return m.EphemerisErr
}

func (m *MockProvider) PushRuntimeUpdate(_ context.Context, target ResolvedRemoteControl, update RuntimeUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RuntimeCalls++
	m.LastRuntime = &update
	m.LastTarget = &target
	// Deep-copy the frame into the history so a later mutation/aliasing of the caller's pointers cannot
	// retroactively change what we recorded for THIS call.
	hist := update
	if update.SatSwitch != nil {
		hist.SatSwitch = update.SatSwitch.DeepCopy()
	}
	if update.Ephemeris.ECEF != nil {
		hist.Ephemeris.ECEF = update.Ephemeris.ECEF.DeepCopy()
	}
	if update.Ephemeris.Orbital != nil {
		hist.Ephemeris.Orbital = update.Ephemeris.Orbital.DeepCopy()
	}
	m.RuntimeHistory = append(m.RuntimeHistory, hist)
	m.TargetHistory = append(m.TargetHistory, target)
	return m.RuntimeErr
}

func (m *MockProvider) Cleanup(_ context.Context, _ *ntnv1alpha1.NTNCellConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CleanupCalls++
	return m.CleanupErr
}
