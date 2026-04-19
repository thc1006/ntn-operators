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
