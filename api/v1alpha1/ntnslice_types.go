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

// NTNSliceSpec defines the desired state of an NTN network slice
// with terrestrial-satellite failover policy.
// +kubebuilder:validation:XValidation:rule="self.terrestrialPath.priority == 'primary'",message="terrestrialPath.priority must be 'primary'"
// +kubebuilder:validation:XValidation:rule="self.satellitePath.priority == 'failover'",message="satellitePath.priority must be 'failover'"
type NTNSliceSpec struct {
	// tenant is the organization or entity that owns this slice.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Tenant string `json:"tenant"`

	// terrestrialPath defines the primary terrestrial connectivity.
	// +required
	TerrestrialPath PathSpec `json:"terrestrialPath"`

	// satellitePath defines the failover satellite connectivity.
	// +required
	SatellitePath SatellitePathSpec `json:"satellitePath"`

	// failoverPolicy defines when and how to switch between paths.
	// +required
	FailoverPolicy FailoverPolicy `json:"failoverPolicy"`

	// qosMapping defines QoS parameter mapping between paths.
	// +optional
	QoSMapping *QoSMapping `json:"qosMapping,omitempty"`

	// security defines handover security requirements.
	// +optional
	Security *SecurityPolicy `json:"security,omitempty"`

	// billing defines CDR generation parameters.
	// +optional
	Billing *BillingSpec `json:"billing,omitempty"`

	// metricsSource selects where the failover engine reads path quality
	// metrics (RSRP, latency, packet loss) from. When omitted, the
	// controller falls back to annotation-driven simulation for backward
	// compatibility with existing development deployments.
	// +optional
	MetricsSource *MetricsSource `json:"metricsSource,omitempty"`
}

// MetricsSourceType enumerates supported metric source backends.
// +kubebuilder:validation:Enum=annotations;prometheus
type MetricsSourceType string

const (
	// MetricsSourceAnnotations reads simulated metric values from the
	// NTNSlice's own annotations (ntn.operators.dev/simulated-*).
	// Intended for development and tests.
	MetricsSourceAnnotations MetricsSourceType = "annotations"

	// MetricsSourcePrometheus reads live metric values from a Prometheus
	// HTTP API. Used in production deployments.
	MetricsSourcePrometheus MetricsSourceType = "prometheus"
)

// MetricsSource configures the failover engine's source of path quality
// measurements. The CEL rule below enforces the implication
// "type=prometheus → prometheus block set" at admission time.
// +kubebuilder:validation:XValidation:rule="self.type != 'prometheus' || has(self.prometheus)",message="prometheus block is required when type is 'prometheus'"
type MetricsSource struct {
	// type is the backend kind.
	// +kubebuilder:default=annotations
	// +optional
	Type MetricsSourceType `json:"type,omitempty"`

	// prometheus configures the Prometheus HTTP API backend.
	// Required when type is 'prometheus'.
	// +optional
	Prometheus *PrometheusMetricsSource `json:"prometheus,omitempty"`
}

// PrometheusMetricsSource describes how to query a Prometheus HTTP API for
// path quality values. Queries are PromQL strings, keeping the operator
// independent of any specific exporter's label dialect.
// +kubebuilder:validation:XValidation:rule="size(self.queries.rsrpDbm) > 0 || size(self.queries.latencyMs) > 0 || size(self.queries.packetLossPercent) > 0",message="at least one query must be non-empty"
type PrometheusMetricsSource struct {
	// endpoint is the base URL of the Prometheus HTTP API.
	// +kubebuilder:validation:Pattern=`^https?://`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	Endpoint string `json:"endpoint"`

	// queryTimeout limits the wall-clock time spent on each individual
	// PromQL fetch; the controller issues up to three fetches per
	// reconcile (one per metric), so the upper bound for a Read is
	// roughly 3x this value. Defaults to 2s when unset.
	// +kubebuilder:validation:Format=duration
	// +optional
	QueryTimeout *metav1.Duration `json:"queryTimeout,omitempty"`

	// queries holds the PromQL expressions for each observable metric.
	// +required
	Queries PrometheusQueries `json:"queries"`
}

// PrometheusQueries carries the PromQL expressions for each of the three
// path-quality metrics. An empty string for a given field means "not
// configured for this slice"; the controller uses the default value for
// that metric instead of issuing a query.
type PrometheusQueries struct {
	// rsrpDbm is a PromQL expression returning a scalar in dBm.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	RsrpDbm string `json:"rsrpDbm,omitempty"`

	// latencyMs is a PromQL expression returning a scalar in milliseconds.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	LatencyMs string `json:"latencyMs,omitempty"`

	// packetLossPercent is a PromQL expression returning a scalar in
	// percent (0-100).
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	PacketLossPercent string `json:"packetLossPercent,omitempty"`
}

// PathSpec defines a network path (terrestrial or satellite).
type PathSpec struct {
	// provider is the network operator name (e.g., "chunghwa-telecom").
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Provider string `json:"provider"`

	// apn is the Access Point Name.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	APN string `json:"apn,omitempty"`

	// priority is the path priority.
	// +kubebuilder:validation:Enum=primary;failover
	Priority string `json:"priority"`
}

// SatellitePathSpec extends PathSpec with satellite-specific fields.
type SatellitePathSpec struct {
	PathSpec `json:",inline"`

	// ephemerisRef is the name of the SatelliteEphemeris resource used to determine
	// satellite pass availability. The referenced ephemeris is treated as a
	// CONSTELLATION POOL: the satellite path is available whenever ANY tracked member
	// is overhead and deliverable — the slice is deliberately not pinned to one NORAD,
	// because LEO members hand over. Which member a given cell broadcasts is
	// NTNCellConfig.ephemerisNoradID's concern, not this path's. See ADR-0008.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	EphemerisRef string `json:"ephemerisRef"`
}

// FailoverPolicy defines the conditions for path switching.
// The trigger-syntax rule below rejects malformed triggers at admission (in every
// install channel, with no webhook/cert), so a typo cannot be silently skipped at
// runtime (findings.md B-2/I-11). It mirrors pkg/slice.ParseTrigger; the regex is
// intentionally a touch stricter for a few exotic value/whitespace forms (hex
// floats, trailing-dot/underscore numbers, non-ASCII spaces) never used for
// thresholds — the divergence test in pkg/slice enumerates them. maxItems=10
// + maxLength keep the CEL cost within budget.
// +kubebuilder:validation:XValidation:rule="self.triggers.all(t, t.matches('^ *(rsrp|latency|packetLoss|terrestrialRSRP|terrestrialLatency|terrestrialPacketLoss) *(<=|>=|<|>) *[-+]?([0-9]{1,10}([.][0-9]{1,10})?|[.][0-9]{1,10})([eE][-+]?[0-9]{1,2})? *$'))",message="each failoverPolicy.trigger must be 'metric op value' where metric is one of rsrp/latency/packetLoss/terrestrialRSRP/terrestrialLatency/terrestrialPacketLoss, op is one of < <= > >=, and value is a finite decimal (up to 10 integer and 10 fraction digits with an optional 2-digit exponent, e.g. 'rsrp < -120'); overflowing forms like '1e9999' are rejected here and, defensively, at runtime"
type FailoverPolicy struct {
	// triggers defines conditions that initiate failover (OR logic).
	// Format: "metric operator value" (e.g., "rsrp < -120").
	// Validated at admission by the XValidation rule on this type and at runtime by
	// the failover engine (pkg/slice.ParseTrigger).
	// Order is intentionally not significant; set merge semantics are desired.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:items:MaxLength=64
	// +listType=set
	Triggers []string `json:"triggers"`

	// switchbackDelay is how long to wait after terrestrial recovers
	// before switching back (prevents flapping). A negative value is meaningless
	// here, so admission bounds it to [0s, 24h] (24h is a fat-finger ceiling, far
	// above any LEO-pass-relative value).
	// +kubebuilder:default="60s"
	// +kubebuilder:validation:Format=duration
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('0s') && duration(self) <= duration('24h')",message="switchbackDelay must be a non-negative duration no greater than 24h"
	SwitchbackDelay metav1.Duration `json:"switchbackDelay,omitempty"`

	// sessionContinuity is accepted for forward compatibility but is NOT enforced by this
	// build: no runtime session preservation is performed during a path switch, so active
	// sessions may drop on failover regardless of this value. It is retained so the intent
	// stays expressible and a future release can honor it (#69); do not rely on it for
	// session survival today. The default stays true only to preserve the field's prior
	// admission behavior — it turns no preservation on.
	// +kubebuilder:default=true
	SessionContinuity bool `json:"sessionContinuity,omitempty"`

	// hysteresisMargin is a dead-band applied to trigger thresholds
	// during switchback evaluation, preventing flapping when metrics
	// oscillate near the threshold. The value uses the same unit as
	// the trigger (dB for RSRP, ms for latency, percent for packetLoss).
	// Example: with trigger "rsrp < -120" and hysteresisMargin "10",
	// failover fires at RSRP < -120, but switchback requires RSRP >= -110.
	// +kubebuilder:validation:Pattern=`^[0-9]+\.?[0-9]*$`
	// +optional
	// +kubebuilder:validation:MaxLength=32
	HysteresisMargin string `json:"hysteresisMargin,omitempty"`

	// confirmationSamples is the number of CONSECUTIVE reconcile samples on which
	// the terrestrial triggers must fire before a failover to satellite is taken
	// (production load balancers such as AWS ALB / GCP count consecutive, not
	// windowed, probe results). It absorbs a single-sample blip so one noisy
	// reading does not trip a switch. 1 (the default when unset) preserves the
	// prior immediate-failover behavior; a value of N delays failover by up to
	// (N-1) reconcile intervals. The confirmation counter is kept in memory and
	// resets on any healthy reliable sample; losing it on a controller restart or
	// leader-election handoff only re-requires confirmation (a DELAY), never causes
	// a spurious switch.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	// +optional
	ConfirmationSamples *int32 `json:"confirmationSamples,omitempty"`

	// minTerrestrialDwell is the minimum time the terrestrial path must be held
	// after a switchback before another failover to satellite may be taken. It
	// bounds sub-minute ping-pong after a hand-back. It is a soft, bounded delay
	// — a genuinely failing terrestrial still fails over once the dwell elapses,
	// so it never indefinitely blocks a real failover. 0 (the default) disables
	// it; 30s–120s is a sane range relative to a LEO pass. A negative value would
	// silently disable the dwell (elapsed < negative is never true), so admission
	// bounds it to [0s, 24h].
	// +kubebuilder:validation:Format=duration
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('0s') && duration(self) <= duration('24h')",message="minTerrestrialDwell must be a non-negative duration no greater than 24h"
	// +optional
	MinTerrestrialDwell metav1.Duration `json:"minTerrestrialDwell,omitempty"`
}

// QoSMapping defines QoS parameter mapping between terrestrial and satellite paths.
type QoSMapping struct {
	// terrestrial5QI is the 5G QoS Identifier for the terrestrial path.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=255
	Terrestrial5QI int `json:"terrestrial5QI,omitempty"`

	// satelliteQCI is the QoS class for the satellite path.
	// +kubebuilder:validation:Enum=conversational;streaming;interactive;background;best-effort
	// +kubebuilder:default="best-effort"
	SatelliteQCI string `json:"satelliteQCI,omitempty"`

	// maxLatencyBudget is the maximum acceptable latency including
	// satellite propagation delay.
	// +kubebuilder:default="150ms"
	// +kubebuilder:validation:Format=duration
	MaxLatencyBudget metav1.Duration `json:"maxLatencyBudget,omitempty"`
}

// SecurityPolicy defines handover security requirements.
type SecurityPolicy struct {
	// encryptionLevel specifies the encryption standard.
	// +kubebuilder:validation:Enum=AES-128;AES-256;SNOW3G;ZUC
	// +kubebuilder:default="AES-256"
	EncryptionLevel string `json:"encryptionLevel,omitempty"`

	// authOnHandover defines authentication behavior during path switch.
	// +kubebuilder:validation:Enum=re-authenticate;continue
	// +kubebuilder:default="re-authenticate"
	AuthOnHandover string `json:"authOnHandover,omitempty"`
}

// BillingSpec defines CDR generation parameters.
type BillingSpec struct {
	// terrestrialRate is the charging model for terrestrial path.
	// +kubebuilder:validation:Enum=per-volume;per-time;flat
	TerrestrialRate string `json:"terrestrialRate,omitempty"`

	// satelliteRate is the charging model for satellite path.
	// +kubebuilder:validation:Enum=per-volume;per-time;per-minute;flat
	SatelliteRate string `json:"satelliteRate,omitempty"`
}

// NTNSliceStatus defines the observed state of NTNSlice.
// ContactCandidate is the deterministically selected constellation member behind a
// FailoverReady evaluation. Deterministic matters: the same inputs must yield the same
// candidate, or two controllers (or one across a restart) would report different members for
// an unchanged constellation and the field would be worse than absent.
type ContactCandidate struct {
	// noradID is the canonical satellite identity. satellite is a display label only.
	NoradID int `json:"noradID"`

	// satellite is the externally sourced display name, bounded and rune-safe. Not unique:
	// correlate on noradID.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Satellite string `json:"satellite,omitempty"`

	// groundStation is the GroundStationLifecycle this window was predicted for. A pass is
	// only meaningful relative to an observer, so a candidate without one is not auditable.
	// +optional
	GroundStation string `json:"groundStation,omitempty"`

	// aos and los bound the pass window this candidate is currently inside.
	// +optional
	AOS *metav1.Time `json:"aos,omitempty"`
	// +optional
	LOS *metav1.Time `json:"los,omitempty"`

	// sourceEpochUnixMs is the epoch of the element set the window was propagated FROM —
	// the age that decides deliverability, not the propagation target below. Absent when no
	// propagatedStates entry matches noradID.
	// +optional
	SourceEpochUnixMs int64 `json:"sourceEpochUnixMs,omitempty"`

	// propagatedEpochUnixMs is the future epoch the state was propagated TO.
	// +optional
	PropagatedEpochUnixMs int64 `json:"propagatedEpochUnixMs,omitempty"`

	// validUntil is when this candidate stops being usable: the EARLIER of the pass ending
	// and its element set aging past the delivery freshness bound. Reporting only LOS would
	// promise a window the runtime push would refuse to use before it closes.
	// +optional
	ValidUntil *metav1.Time `json:"validUntil,omitempty"`

	// selectionReason names the rule that chose this candidate over its siblings, so the
	// choice is reproducible from the status alone.
	// +optional
	SelectionReason string `json:"selectionReason,omitempty"`
}

type NTNSliceStatus struct {
	// activePathType is the currently active network path.
	// +kubebuilder:validation:Enum=terrestrial;satellite;unavailable
	// +optional
	ActivePathType string `json:"activePathType,omitempty"`

	// lastFailover is the timestamp of the last failover event.
	// +optional
	LastFailover *metav1.Time `json:"lastFailover,omitempty"`

	// lastSwitchbackTime is when the slice last switched back to terrestrial from an
	// available satellite (a quality-driven hand-back). It is persisted so the anti-flap
	// minimum-terrestrial-dwell survives a controller restart or leader-election handoff:
	// the in-memory flap clock resets on handoff, and without this a re-degradation within
	// the dwell window could switch to satellite earlier than the dwell intended. The other
	// flap clocks stay in-memory (losing them only delays a switch, never advances one).
	// +optional
	LastSwitchbackTime *metav1.Time `json:"lastSwitchbackTime,omitempty"`

	// failoverCount is the total number of failover events since creation.
	// +optional
	FailoverCount int `json:"failoverCount,omitempty"`

	// sessionCount is the number of active sessions on this slice.
	// +optional
	SessionCount int `json:"sessionCount,omitempty"`

	// appliedQoS summarizes the QoS mapping in effect for the current path.
	// +optional
	AppliedQoS string `json:"appliedQoS,omitempty"`

	// contactCandidate is the constellation member behind the current FailoverReady
	// evaluation: which satellite, seen from which ground station, over which window, and
	// on element data of what age. FailoverReady alone says a contact opportunity exists
	// but not which one, so an operator cannot tell a healthy handover between members
	// from the same member re-evaluated, and cannot audit the decision after the fact.
	//
	// It is a CONTACT OPPORTUNITY, not delivered service: nothing here says the slice's
	// serving cell is configured for this member. Cleared when no candidate can be proven,
	// so a stale entry can never outlive the condition it explains (ADR-0008).
	// +optional
	ContactCandidate *ContactCandidate `json:"contactCandidate,omitempty"`

	// appliedEncryption is the encryption level in effect for the current path.
	// +optional
	AppliedEncryption string `json:"appliedEncryption,omitempty"`

	// billingMode is the billing model active for the current path.
	// +optional
	BillingMode string `json:"billingMode,omitempty"`

	// conditions represent the current state of the slice.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for NTNSlice.
const (
	ConditionPathActive    = "PathActive"
	ConditionFailoverReady = "FailoverReady"
	ConditionQoSApplied    = "QoSApplied"
	ConditionSecured       = "Secured"
	ConditionBillingActive = "BillingActive"

	// ConditionMetricsStale reports whether the most recent reconcile
	// was served from the stale-value cache rather than a fresh
	// metrics source read. Status=True is set while the source is
	// degraded and cleared back to False on the first fresh Read.
	// Pairing the condition with an event emitted only on transition
	// keeps the event stream quiet during long outages.
	ConditionMetricsStale = "MetricsStale"

	// ConditionTriggersReady is False when one or more failoverPolicy.triggers
	// reference a metric that has no configured source (no Prometheus query, no
	// annotation), so the trigger is armed-but-dead: it can never fire against
	// the healthy placeholder (findings.md I-10). True when every trigger's
	// metric is sourced. It surfaces a silent misconfiguration an operator would
	// otherwise only discover when a failover that should have happened did not.
	ConditionTriggersReady = "TriggersReady"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nts
// +kubebuilder:printcolumn:name="Tenant",type=string,JSONPath=`.spec.tenant`
// +kubebuilder:printcolumn:name="Active Path",type=string,JSONPath=`.status.activePathType`
// +kubebuilder:printcolumn:name="Failovers",type=integer,JSONPath=`.status.failoverCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="FailoverReady")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NTNSlice manages terrestrial-satellite network slice failover,
// QoS mapping, and session continuity for NTN enterprise services.
type NTNSlice struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec NTNSliceSpec `json:"spec"`

	// +optional
	Status NTNSliceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NTNSliceList contains a list of NTNSlice.
type NTNSliceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NTNSlice `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &NTNSlice{}, &NTNSliceList{})
		return nil
	})
}
