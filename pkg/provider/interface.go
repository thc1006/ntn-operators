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
	"crypto/tls"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

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

// CellIdentity identifies an OCUDU cell for runtime remote commands.
type CellIdentity struct {
	// PLMN is the cell's PLMN string (e.g. "00101").
	PLMN string
	// NCI is the NR Cell Identity.
	NCI uint64
}

// ResolvedRemoteControl is a gNB's resolved remote-control WebSocket endpoint,
// derived from NTNCellConfig.spec.provider.remoteControl. OCUDU's remote_control
// server is plaintext and unauthenticated by default (localhost sidecar), so
// without TLSConfig the safe deployment is a sidecar next to the gNB pod and
// cross-pod requires a NetworkPolicy. Set TLSConfig (+ optionally AuthToken) to
// reach a wss://-fronted / authenticated endpoint (N-12).
type ResolvedRemoteControl struct {
	// Endpoint is host:port of the gNB remote_control server (e.g. 127.0.0.1:8001).
	Endpoint string
	// TLSConfig, when non-nil, makes the provider dial wss:// with this config
	// (server-certificate verification via RootCAs; optional client certificate for
	// mTLS). Nil keeps the plaintext ws:// dial, preserving existing behavior.
	TLSConfig *tls.Config
	// AuthToken, when non-empty, is sent as an "Authorization: Bearer <token>"
	// header on the WebSocket handshake. It is a replayable shared secret, so it is
	// only ever attached when TLSConfig is set (i.e. over wss://).
	AuthToken string
}

// RuntimeUpdate is a runtime NTN configuration update for a single cell, pushed
// via OCUDU's ntn_config_update remote command (MR !798). It currently carries a
// fresh ephemeris (the #176 use case) plus the fields that command requires.
type RuntimeUpdate struct {
	// Cell identifies the target cell (plmn + nci) OCUDU keys the update on.
	Cell CellIdentity
	// EpochUnixMs is the ephemeris epoch in Unix milliseconds; OCUDU requires it
	// to be in the future.
	EpochUnixMs int64
	// UlSyncValidityDur is the ntn-UlSyncValidityDuration value in seconds.
	UlSyncValidityDur int
	// Ephemeris is the fresh state vector (ECEF or orbital).
	Ephemeris EphemerisUpdate
	// SatSwitch, when non-nil, adds a sat_switch_with_resync block to the push:
	// the next satellite's assistance info, including k_mac (issue #52) — the
	// only OCUDU surface that accepts k_mac. Rides alongside the serving-cell
	// ephemeris in the same per-cell update (OCUDU requires cell ephemeris_info
	// regardless). nil means no pending switch.
	SatSwitch *ntnv1alpha1.SatSwitchWithResync
}

// ErrRuntimeUnsupported is returned when a provider has no runtime push transport
// or the target has no remote-control endpoint configured. Callers fall back to
// the ConfigMap bootstrap path.
var ErrRuntimeUnsupported = errors.New("runtime NTN config push is not supported for this target")

// ErrRuntimePushRejected wraps a PERMANENT runtime-push failure: the gNB rejected
// the config, or the frame was malformed/oversized. Retrying without a config or
// ephemeris change will not help, so callers should not tight-requeue on it
// (unlike a transient unreachable-endpoint error). Test with errors.Is.
var ErrRuntimePushRejected = errors.New("runtime NTN config push permanently rejected")

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

	// PushEphemerisUpdate pushes fresh ephemeris data to the backend by rewriting
	// the bootstrap ConfigMap (the gNB must reload). Kept as the baseline path.
	PushEphemerisUpdate(ctx context.Context, crName, namespace string, update EphemerisUpdate) error

	// PushRuntimeUpdate pushes a runtime NTN config update to the gNB's
	// remote-control WebSocket (OCUDU ntn_config_update, MR !798) — a live update
	// with no ConfigMap rewrite / reload. Returns ErrRuntimeUnsupported when the
	// provider or target has no runtime transport configured.
	PushRuntimeUpdate(ctx context.Context, target ResolvedRemoteControl, update RuntimeUpdate) error

	// EnsureOwnership sets ownership metadata (e.g., OwnerReference) on
	// the provider's managed artifact so it is garbage-collected when
	// the parent NTNCellConfig CR is deleted.
	EnsureOwnership(ctx context.Context, crName string, owner metav1.Object, scheme *runtime.Scheme) error

	// Cleanup removes provider-managed artifacts for the given CR name
	// and namespace. Called by the finalizer during CR deletion.
	// Returns nil if there is nothing to clean up (artifact not found).
	Cleanup(ctx context.Context, crName, namespace string) error
}
