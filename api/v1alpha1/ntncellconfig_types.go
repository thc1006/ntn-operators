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

// NTNCellConfigSpec defines the desired NTN cell configuration.
type NTNCellConfigSpec struct {
	// provider specifies which NTN backend to configure.
	// +required
	Provider ProviderRef `json:"provider"`

	// ntn contains NTN-specific radio parameters per 3GPP TS 38.213 / OCUDU geo_ntn.yml.
	// +required
	NTN NTNParams `json:"ntn"`

	// cellOverrides allows fine-tuning PUCCH, PDSCH, PRACH, and RRC parameters.
	// +optional
	CellOverrides *CellOverrides `json:"cellOverrides,omitempty"`
}

// ProviderRef identifies the NTN backend provider.
type ProviderRef struct {
	// type is the provider type.
	// +kubebuilder:validation:Enum=ocudu;oai;aalyria
	Type string `json:"type"`

	// namespace where the provider resources (e.g., OCUDU gNB) are deployed.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// endpoint is the provider-specific endpoint (e.g., O1 NETCONF address).
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
}

// NTNParams defines the core NTN radio parameters.
// +kubebuilder:validation:XValidation:rule="self.ephemerisECEF.posX != 0 || self.ephemerisECEF.posY != 0 || self.ephemerisECEF.posZ != 0",message="ephemerisECEF position must not be all zeros"
type NTNParams struct {
	// cellSpecificKoffset sets the cell-specific k-offset for NTN (0-1023).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1023
	// +kubebuilder:default=150
	CellSpecificKoffset int `json:"cellSpecificKoffset,omitempty"`

	// taCommon sets the common Timing Advance value (0-66485757).
	// Deprecated: use taInfo.taCommon instead. Kept for backward compatibility;
	// if taInfo is set, taInfo.taCommon takes precedence.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=66485757
	// +kubebuilder:default=0
	TACommon int `json:"taCommon,omitempty"`

	// taInfo provides extended Timing Advance parameters per 3GPP TS 38.213.
	// When set, taInfo.taCommon takes precedence over the top-level taCommon field.
	// +optional
	TAInfo *TAInfo `json:"taInfo,omitempty"`

	// ephemerisECEF defines the satellite position and velocity in ECEF coordinates.
	// +required
	EphemerisECEF EphemerisECEF `json:"ephemerisECEF"`

	// movingRefLocation defines the Earth-moving reference location for LEO NTN cells.
	// 3GPP Release 18 SIB19 field. Used by UEs for timing/Doppler estimation.
	// +optional
	MovingRefLocation *MovingRefLocation `json:"movingRefLocation,omitempty"`

	// satSwitchWithResync provides satellite switch handover hints to UEs during
	// satellite-to-satellite transitions. 3GPP Release 18 SIB19 field.
	// +optional
	SatSwitchWithResync *SatSwitchWithResync `json:"satSwitchWithResync,omitempty"`

	// payloadType specifies the satellite payload architecture.
	// +kubebuilder:validation:Enum=transparent;regenerative
	// +kubebuilder:default="transparent"
	PayloadType string `json:"payloadType,omitempty"`
}

// TAInfo provides extended Timing Advance parameters per 3GPP TS 38.213.
// Maps to OCUDU's ta_info YAML block.
type TAInfo struct {
	// taCommon is the common Timing Advance value (0-66485757).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=66485757
	// +kubebuilder:default=0
	TACommon int `json:"taCommon,omitempty"`

	// taCommonDrift is the TA drift rate in units of TA per second.
	// Used for LEO satellites where TA changes continuously.
	// +optional
	TACommonDrift int `json:"taCommonDrift,omitempty"`

	// taCommonDriftVariant is the TA drift rate variant.
	// +optional
	TACommonDriftVariant int `json:"taCommonDriftVariant,omitempty"`

	// taCommonOffset is an additional TA offset.
	// +optional
	TACommonOffset int `json:"taCommonOffset,omitempty"`
}

// MovingRefLocation defines the Earth-moving reference location for LEO NTN cells.
// 3GPP Release 18 SIB19. Values in 1e-4 degrees per 3GPP TS 38.331.
type MovingRefLocation struct {
	// latitude in 1e-4 degrees (-900000 to 900000 = -90° to 90°).
	// +kubebuilder:validation:Minimum=-900000
	// +kubebuilder:validation:Maximum=900000
	Latitude int `json:"latitude"`

	// longitude in 1e-4 degrees (-1800000 to 1800000 = -180° to 180°).
	// +kubebuilder:validation:Minimum=-1800000
	// +kubebuilder:validation:Maximum=1800000
	Longitude int `json:"longitude"`
}

// SatSwitchWithResync provides handover hints for satellite-to-satellite transitions.
// 3GPP Release 18 SIB19. Allows UEs to prepare for serving cell change when
// the current satellite leaves coverage.
type SatSwitchWithResync struct {
	// targetPCI is the Physical Cell Identity of the target cell after switch (0-1007).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1007
	TargetPCI int `json:"targetPCI"`

	// t304 is the handover timer value in milliseconds per 3GPP TS 38.331.
	// +kubebuilder:validation:Enum=50;100;150;200;500;1000;2000;10000
	T304 int `json:"t304"`
}

// EphemerisECEF defines the satellite position and velocity in Earth-Centered
// Earth-Fixed coordinates. For GEO satellites, velocity fields should be 0.
type EphemerisECEF struct {
	// posX is the X position of the satellite (-67108864 to 67108863).
	// +kubebuilder:validation:Minimum=-67108864
	// +kubebuilder:validation:Maximum=67108863
	PosX int `json:"posX"`
	// posY is the Y position of the satellite (-67108864 to 67108863).
	// +kubebuilder:validation:Minimum=-67108864
	// +kubebuilder:validation:Maximum=67108863
	PosY int `json:"posY"`
	// posZ is the Z position of the satellite (-67108864 to 67108863).
	// +kubebuilder:validation:Minimum=-67108864
	// +kubebuilder:validation:Maximum=67108863
	PosZ int `json:"posZ"`
	// velX is the X velocity of the satellite (0 for GEO).
	// +kubebuilder:default=0
	VelX int `json:"velX,omitempty"`
	// velY is the Y velocity of the satellite (0 for GEO).
	// +kubebuilder:default=0
	VelY int `json:"velY,omitempty"`
	// velZ is the Z velocity of the satellite (0 for GEO).
	// +kubebuilder:default=0
	VelZ int `json:"velZ,omitempty"`
}

// CellOverrides allows fine-tuning cell parameters for NTN.
// Note: PUCCH SR period is intentionally NOT exposed; the gNB
// auto-selects the correct value based on channel bandwidth.
type CellOverrides struct {
	// pdschMaxHarqRetxs sets the max HARQ retransmissions (0 = disabled for NTN).
	// +kubebuilder:default=0
	PdschMaxHarqRetxs int `json:"pdschMaxHarqRetxs,omitempty"`

	// prachMaxMsg3HarqRetx sets the max msg3 HARQ retransmissions.
	// +kubebuilder:default=0
	PrachMaxMsg3HarqRetx int `json:"prachMaxMsg3HarqRetx,omitempty"`

	// rrcGuardTimeMs sets the RRC procedure guard time in ms.
	// +kubebuilder:default=12800
	RrcGuardTimeMs int `json:"rrcGuardTimeMs,omitempty"`
}

// NTNCellConfigStatus defines the observed state of NTNCellConfig.
type NTNCellConfigStatus struct {
	// appliedKoffset is the last successfully applied k-offset value.
	// +optional
	AppliedKoffset int `json:"appliedKoffset,omitempty"`

	// configMapRef is the name of the ConfigMap containing the generated config.
	// +optional
	ConfigMapRef string `json:"configMapRef,omitempty"`

	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for NTNCellConfig.
const (
	ConditionConfigApplied = "ConfigApplied"
	ConditionConfigValid   = "ConfigValid"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ntncc
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider.type`
// +kubebuilder:printcolumn:name="Koffset",type=integer,JSONPath=`.spec.ntn.cellSpecificKoffset`
// +kubebuilder:printcolumn:name="Payload",type=string,JSONPath=`.spec.ntn.payloadType`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NTNCellConfig manages NTN-specific radio parameters for a gNB cell,
// delegating configuration to the specified NTN backend provider (OCUDU, OAI, Aalyria).
type NTNCellConfig struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec NTNCellConfigSpec `json:"spec"`

	// +optional
	Status NTNCellConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NTNCellConfigList contains a list of NTNCellConfig.
type NTNCellConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NTNCellConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NTNCellConfig{}, &NTNCellConfigList{})
}
