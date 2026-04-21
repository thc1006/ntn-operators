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
)

// NTNSliceSpec defines the desired state of an NTN network slice
// with terrestrial-satellite failover policy.
// +kubebuilder:validation:XValidation:rule="self.terrestrialPath.priority == 'primary'",message="terrestrialPath.priority must be 'primary'"
// +kubebuilder:validation:XValidation:rule="self.satellitePath.priority == 'failover'",message="satellitePath.priority must be 'failover'"
type NTNSliceSpec struct {
	// tenant is the organization or entity that owns this slice.
	// +kubebuilder:validation:MinLength=1
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
	Endpoint string `json:"endpoint"`

	// queryTimeout limits the wall-clock time spent on each individual
	// PromQL fetch; the controller issues up to three fetches per
	// reconcile (one per metric), so the upper bound for a Read is
	// roughly 3x this value. Defaults to 2s when unset.
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
	RsrpDbm string `json:"rsrpDbm,omitempty"`

	// latencyMs is a PromQL expression returning a scalar in milliseconds.
	// +optional
	LatencyMs string `json:"latencyMs,omitempty"`

	// packetLossPercent is a PromQL expression returning a scalar in
	// percent (0-100).
	// +optional
	PacketLossPercent string `json:"packetLossPercent,omitempty"`
}

// PathSpec defines a network path (terrestrial or satellite).
type PathSpec struct {
	// provider is the network operator name (e.g., "chunghwa-telecom").
	// +kubebuilder:validation:MinLength=1
	Provider string `json:"provider"`

	// apn is the Access Point Name.
	// +optional
	APN string `json:"apn,omitempty"`

	// priority is the path priority.
	// +kubebuilder:validation:Enum=primary;failover
	Priority string `json:"priority"`
}

// SatellitePathSpec extends PathSpec with satellite-specific fields.
type SatellitePathSpec struct {
	PathSpec `json:",inline"`

	// ephemerisRef is the name of the SatelliteEphemeris resource
	// used to determine satellite pass availability.
	// +kubebuilder:validation:MinLength=1
	EphemerisRef string `json:"ephemerisRef"`
}

// FailoverPolicy defines the conditions for path switching.
type FailoverPolicy struct {
	// triggers defines conditions that initiate failover (OR logic).
	// Format: "metric operator value" (e.g., "rsrp < -120").
	// Validated at runtime by the failover engine (pkg/slice.ParseTrigger).
	// Order is intentionally not significant; set merge semantics are desired.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=10
	// +listType=set
	Triggers []string `json:"triggers"`

	// switchbackDelay is how long to wait after terrestrial recovers
	// before switching back (prevents flapping).
	// +kubebuilder:default="60s"
	// +kubebuilder:validation:Format=duration
	SwitchbackDelay metav1.Duration `json:"switchbackDelay,omitempty"`

	// sessionContinuity preserves active sessions during failover.
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
	HysteresisMargin string `json:"hysteresisMargin,omitempty"`
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
type NTNSliceStatus struct {
	// activePathType is the currently active network path.
	// +kubebuilder:validation:Enum=terrestrial;satellite;unavailable
	// +optional
	ActivePathType string `json:"activePathType,omitempty"`

	// lastFailover is the timestamp of the last failover event.
	// +optional
	LastFailover *metav1.Time `json:"lastFailover,omitempty"`

	// failoverCount is the total number of failover events since creation.
	// +optional
	FailoverCount int `json:"failoverCount,omitempty"`

	// sessionCount is the number of active sessions on this slice.
	// +optional
	SessionCount int `json:"sessionCount,omitempty"`

	// appliedQoS summarizes the QoS mapping in effect for the current path.
	// +optional
	AppliedQoS string `json:"appliedQoS,omitempty"`

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
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=nts
// +kubebuilder:printcolumn:name="Tenant",type=string,JSONPath=`.spec.tenant`
// +kubebuilder:printcolumn:name="Active Path",type=string,JSONPath=`.status.activePathType`
// +kubebuilder:printcolumn:name="Failovers",type=integer,JSONPath=`.status.failoverCount`
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
	SchemeBuilder.Register(&NTNSlice{}, &NTNSliceList{})
}
