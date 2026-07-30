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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EphemerisSource defines where to fetch GP (General Perturbations) data from.
// Supports CelesTrak OMM JSON and Space-Track.org OMM JSON formats.
// +kubebuilder:validation:XValidation:rule="self.type != 'SpaceTrack' || has(self.credentials)",message="SpaceTrack source type requires credentials (spec.source.credentials)"
// Note: refreshInterval minimum (2h) is enforced at runtime by the controller
// (minRefreshInterval constant) because metav1.Duration is serialized as a
// string in CRD schema and CEL cannot parse Go duration strings.
type EphemerisSource struct {
	// type is the source type. Supported: "CelesTrak", "SpaceTrack".
	// +kubebuilder:validation:Enum=CelesTrak;SpaceTrack
	Type string `json:"type"`

	// url is the endpoint to fetch GP data from. Use https for any public
	// source: a cleartext http:// URL that resolves to a public IP is refused
	// at runtime (InsecureURL condition) because an on-path attacker could
	// inject forged OMM data that is propagated into SIB19. http:// is permitted
	// only for a private/in-cluster mirror (NetworkPolicy-protected).
	// For CelesTrak: https://celestrak.org/NORAD/elements/gp.php?GROUP=oneweb&FORMAT=JSON
	// For SpaceTrack: https://www.space-track.org/basicspacedata/query/class/gp/...
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^https?://`
	// +kubebuilder:validation:MaxLength=2048
	URL string `json:"url"`

	// refreshInterval is how often to re-fetch GP data.
	// CelesTrak updates every 2 hours; setting this below 2h wastes bandwidth.
	// +kubebuilder:default="4h"
	// +kubebuilder:validation:Format=duration
	RefreshInterval metav1.Duration `json:"refreshInterval"`

	// credentials is a reference to a Secret containing auth credentials
	// (required for SpaceTrack, optional for CelesTrak).
	// +optional
	Credentials *SecretReference `json:"credentials,omitempty"`
}

// SecretReference points to a Kubernetes Secret.
type SecretReference struct {
	// name of the Secret.
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// key within the Secret data.
	// +kubebuilder:default="password"
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key,omitempty"`
}

// SatelliteSelector defines which satellites to track.
type SatelliteSelector struct {
	// constellation is DEPRECATED and performs no filtering at all — the controller
	// has never consumed it (neither server- nor client-side). Select a constellation
	// in the source URL instead (CelesTrak's GROUP= query parameter, e.g. GROUP=oneweb,
	// returns only that constellation's element sets) and/or list explicit noradIDs.
	// It stays accepted in v1alpha1; removal is deferred to a future versioned API
	// migration — a v1alpha2 that drops it must ship conversion so v1alpha1<->v1alpha2
	// round-trips losslessly, plus stored-object migration and storedVersions cleanup;
	// a version rename alone is not enough to safely drop the data.
	//
	// Deprecated: select the constellation via source.url (GROUP=) or spec.satellites.noradIDs.
	// +optional
	Constellation string `json:"constellation,omitempty"`

	// noradIDs is an explicit list of NORAD catalog IDs to track.
	// +optional
	// +kubebuilder:validation:MaxItems=512
	NoradIDs []int `json:"noradIDs,omitempty"`
}

// PassPredictionSpec configures pass prediction calculations.
type PassPredictionSpec struct {
	// groundStations is a list of GroundStationLifecycle resource names
	// to compute pass windows against.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=253
	GroundStations []string `json:"groundStations"`

	// minElevation is the minimum elevation angle in degrees (string, e.g., "10"), in the
	// range [0, 90]: 0 is the geometric horizon and 90 the zenith. A negative or >90 mask
	// is physically meaningless (it would make every / no pass "usable"), so the pattern
	// rejects negatives and the CEL rule rejects values above 90. The pattern keeps the
	// pre-existing grammar otherwise (a trailing "." such as "10." stays valid — only the
	// leading "-" was removed) to avoid an unrelated API-grammar break. The bound is on the
	// parsed float64 value (the pipeline is float64), so a literal within ~half a ULP above
	// 90 rounds to 90 and is accepted; the controller uses the float64 value.
	// +kubebuilder:default="10"
	// +kubebuilder:validation:Pattern=`^[0-9]+\.?[0-9]*$`
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:XValidation:rule="double(self) <= 90.0",message="minElevation must be between 0 and 90 degrees"
	MinElevation string `json:"minElevation,omitempty"`

	// horizon is how far into the future to predict passes.
	// +kubebuilder:default="24h"
	Horizon metav1.Duration `json:"horizon,omitempty"`
}

// SatelliteEphemerisSpec defines the desired state of SatelliteEphemeris.
type SatelliteEphemerisSpec struct {
	// source defines where to fetch GP (General Perturbations) data.
	// +required
	Source EphemerisSource `json:"source"`

	// satellites filters which satellites to track from the source.
	// +optional
	Satellites *SatelliteSelector `json:"satellites,omitempty"`

	// passPrediction configures automatic pass window computation.
	// +optional
	PassPrediction *PassPredictionSpec `json:"passPrediction,omitempty"`
}

// PassWindow represents a predicted contact opportunity between a satellite and ground station.
type PassWindow struct {
	// satellite is the satellite's display name (externally sourced OMM ObjectName), bounded and
	// rune-safe. The canonical identity is noradID; treat this as a label only.
	// +kubebuilder:validation:MaxLength=64
	Satellite string `json:"satellite"`

	// noradID is the satellite's NORAD catalog number — the canonical key that maps this window
	// to its propagatedStates entry, so a consumer (NTNSlice) can gate pass availability on the
	// backing element set's delivery freshness (same contract as the runtime push). 0 only for
	// windows written before this field existed (freshness then treated as unverifiable).
	// +optional
	NoradID int `json:"noradID,omitempty"`

	// groundStation is the name of the GroundStationLifecycle resource.
	GroundStation string `json:"groundStation"`

	// aos is the Acquisition of Signal time (satellite rises above minElevation).
	AOS metav1.Time `json:"aos"`

	// los is the Loss of Signal time (satellite drops below minElevation).
	LOS metav1.Time `json:"los"`

	// maxElevation is the peak elevation angle during the pass in degrees (string, e.g., "72.5").
	// +kubebuilder:validation:Pattern=`^-?[0-9]+\.?[0-9]*$`
	MaxElevation string `json:"maxElevation"`
}

// PropagatedState is a satellite state vector propagated (SGP4) to a specific
// epoch, in the 3GPP ECEF codepoint form the runtime ephemeris push consumes.
type PropagatedState struct {
	// satellite is the satellite name or object ID (bounded; the controller
	// truncates the externally-sourced name to this length).
	// +kubebuilder:validation:MaxLength=64
	Satellite string `json:"satellite"`
	// noradID is the satellite's NORAD catalog number, used by NTNCellConfig
	// (spec.ephemerisNoradID) to select which state to push.
	NoradID int `json:"noradID"`
	// epochUnixMs is the propagation epoch in Unix milliseconds (in the future,
	// as OCUDU's ntn_config_update requires).
	EpochUnixMs int64 `json:"epochUnixMs"`
	// sourceEpochUnixMs is the epoch of the SOURCE orbital element set (the OMM EPOCH)
	// this state was propagated FROM, in Unix milliseconds. Unlike epochUnixMs (the
	// future propagation target), this is the element-set age used by the runtime-push
	// consumer to refuse pushing THIS satellite's drifting elements — a per-satellite
	// freshness bound, so a stale sibling in the same SatelliteEphemeris does not block
	// this one. The producer no longer emits a state whose source epoch it could not parse
	// (those satellites are skipped and surfaced via the SourceEpochRejected condition), so a 0
	// here is the zero value of an omitted field; the consumer treats a 0 epoch as the 1970
	// instant subject to the normal staleness bound, not as "unknown" or fresh.
	// +optional
	SourceEpochUnixMs int64 `json:"sourceEpochUnixMs,omitempty"`
	// ecef is the propagated position/velocity in 3GPP codepoints. The provider
	// converts these to physical SI when pushing to OCUDU.
	ECEF EphemerisECEF `json:"ecef"`
}

// SatelliteEphemerisStatus defines the observed state of SatelliteEphemeris.
type SatelliteEphemerisStatus struct {
	// propagatedStatesInputHash is a digest of the spec fields that determine WHICH
	// orbital data is fetched and WHICH satellites are propagated (source type/url and the
	// NORAD selector) — NOT the whole spec. It is stamped whenever the current
	// propagatedStates are (re)computed, from a fresh fetch or a valid cache entry for the
	// same upstream fetch identity (the OMM cache is keyed on source type+url, NOT on
	// .metadata.generation). The NTNCellConfig runtime-push consumer recomputes the hash from the live
	// spec and refuses to push when it differs — i.e. the persisted states were computed
	// under different propagation inputs (a source/selector edit whose re-propagate has
	// not yet succeeded), WITHOUT falsely invalidating on a pass-prediction-only edit
	// (#204-G1). Empty means the states predate this field (never re-propagated since
	// upgrade) or no successful reconcile yet.
	// +optional
	PropagatedStatesInputHash string `json:"propagatedStatesInputHash,omitempty"`

	// lastUpdated is when the GP data was last successfully fetched.
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`

	// lastPassPredictionTime is when the pass-window sweep last ran. The sweep runs on its own
	// lower cadence (passPredictionInterval), decoupled from the propagation heartbeat, so its
	// O(horizon x satellites x ground stations) cost stays out of the runtime-push epoch cadence
	// (ADR 0006 / #234). Persisted rather than in-memory so the "sweep is due" decision survives a
	// leader failover. Absent means the sweep has not run since this field appeared (or ever).
	// +optional
	LastPassPredictionTime *metav1.Time `json:"lastPassPredictionTime,omitempty"`

	// lastPassPredictionInputHash is a digest of the inputs that determine the pass windows: the
	// pass-prediction spec (ground stations, minElevation, horizon), the tracked NORAD selector, the
	// source identity, and each resolved ground station's identity/generation. The sweep re-runs
	// IMMEDIATELY when this changes — not only on the passPredictionInterval time cadence — so a
	// ground-station edit/add/delete, a selector change, or an elevation/horizon change re-predicts at
	// once instead of leaving stale windows for up to an interval (ADR 0006 / #234). Cleared whenever
	// the pass windows are invalidated (see invalidatePassPredictionStatus).
	// +optional
	LastPassPredictionInputHash string `json:"lastPassPredictionInputHash,omitempty"`

	// satelliteCount is the number of satellites currently tracked.
	// +optional
	SatelliteCount int `json:"satelliteCount,omitempty"`

	// truncatedSatelliteCount is how many selected satellites were NOT propagated
	// because the maxPropagatedStates cap (128) had already been reached — the count
	// actually dropped by the cap, not merely (selected - 128), so satellites that
	// fail SGP4 propagation do not inflate it. ABSENT or 0 means nothing was dropped
	// (the field is omitempty). Narrow spec.satellites.noradIDs or the source URL's
	// GROUP= to eliminate it. Mirrored by the StatesTruncated condition; a Warning
	// StatesTruncated event fires once per transition into the truncated state.
	// +optional
	TruncatedSatelliteCount int `json:"truncatedSatelliteCount,omitempty"`

	// nextPassWindows contains upcoming contact opportunities.
	// +optional
	NextPassWindows []PassWindow `json:"nextPassWindows,omitempty"`

	// propagatedStates holds SGP4-propagated ECEF state vectors (per satellite) at
	// the last refresh epoch, consumed by NTNCellConfig runtime ephemeris push (#176).
	// Capped (maxItems) to match the controller's maxPropagatedStates and stay well
	// under the etcd object-size limit.
	// +kubebuilder:validation:MaxItems=128
	// +optional
	PropagatedStates []PropagatedState `json:"propagatedStates,omitempty"`

	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for SatelliteEphemeris.
const (
	ConditionGPDataFetched   = "GPDataFetched"
	ConditionGPDataParsed    = "GPDataParsed"
	ConditionPassesPredicted = "PassesPredicted"
	// ConditionStatesTruncated is True when the selected satellite set exceeded the
	// maxPropagatedStates cap and some were omitted from the runtime-push status.
	// False means every selected satellite fit within the cap.
	ConditionStatesTruncated = "StatesTruncated"
	// ConditionUnsupportedOrbitRegime is True when the source contained element
	// sets whose orbital period is >= 225 min (deep space, roughly MEO and up).
	// The v1.0 propagator is near-earth SGP4 only, so those sets are rejected
	// rather than propagated into a wrong position; MEO/GEO support is a v1.1
	// roadmap item. False means every tracked element set is near-earth.
	ConditionUnsupportedOrbitRegime = "UnsupportedOrbitRegime"
	// ConditionEphemerisEpochStale is True when one or more propagated element
	// sets have an epoch older than the freshness bound, so the pushed ECEF is
	// derived from stale elements and drifting (SGP4 in-track error grows with
	// age from the element epoch). False means all propagated epochs are fresh.
	ConditionEphemerisEpochStale = "EphemerisEpochStale"
	// ConditionSourceEpochRejected is True when one or more tracked element sets were
	// REFUSED BEFORE propagation because their OMM EPOCH is unparseable or implausibly
	// future-dated (a corrupt or spoofed feed — SGP4 would otherwise be driven backward from
	// the bogus epoch). Such a satellite produces NO propagated state, so a cell selecting it
	// reports a bare EphemerisPayloadMissing; this condition is what tells the operator the
	// cause is bad source data rather than a NORAD typo or a deep-space rejection. False
	// means every tracked element set has a parseable, plausibly-dated epoch. Distinct from
	// EphemerisEpochStale, which covers merely OLD (but still propagated) element sets.
	ConditionSourceEpochRejected = "SourceEpochRejected"
	// ConditionRefreshIntervalClamped is True when spec.source.refreshInterval was
	// outside [2h, 24h] and the controller clamped it (Reason BelowMinimum /
	// AboveMaximum). False (Reason WithinBounds) means the controller evaluated the
	// interval and used it as-is. Absent means not yet reconciled (Unknown) — the
	// controller always sets it explicitly, so False and absent are distinct. It lets
	// the clamp be surfaced once per episode instead of a Warning every reconcile.
	ConditionRefreshIntervalClamped = "RefreshIntervalClamped"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sateph
// +kubebuilder:printcolumn:name="Satellites",type=integer,JSONPath=`.status.satelliteCount`
// +kubebuilder:printcolumn:name="Last Updated",type=date,JSONPath=`.status.lastUpdated`
// +kubebuilder:printcolumn:name="Fetched",type=string,JSONPath=`.status.conditions[?(@.type=="GPDataFetched")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SatelliteEphemeris manages GP data fetching (OMM JSON from CelesTrak/SpaceTrack),
// orbital propagation (SGP4 via akhenakh/sgp4), and pass prediction for a set of
// satellites against ground stations.
//
// Orbit-regime support: v1.0 is LEO-only. The propagator is the near-earth SGP4
// model; element sets whose orbital period is >= 225 minutes (deep space —
// roughly MEO and above, e.g. O3b or GEO) are rejected rather than propagated
// into a wrong position, and surface as the UnsupportedOrbitRegime status
// condition. Multi-orbit (MEO/GEO) support is a v1.1 roadmap item.
type SatelliteEphemeris struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SatelliteEphemerisSpec `json:"spec"`

	// +optional
	Status SatelliteEphemerisStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SatelliteEphemerisList contains a list of SatelliteEphemeris.
type SatelliteEphemerisList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SatelliteEphemeris `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &SatelliteEphemeris{}, &SatelliteEphemerisList{})
		return nil
	})
}
