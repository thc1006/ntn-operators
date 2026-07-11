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
	return m.RuntimeErr
}

func (m *MockProvider) Cleanup(_ context.Context, _ *ntnv1alpha1.NTNCellConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CleanupCalls++
	return m.CleanupErr
}
