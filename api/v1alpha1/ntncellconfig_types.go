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

	// ephemerisRef is the name of a SatelliteEphemeris CR in the same namespace.
	// When set, the controller re-reconciles this NTNCellConfig whenever the
	// referenced SatelliteEphemeris is updated. Future work will consume the
	// ephemeris data for dynamic NTN parameter updates; currently this field
	// only triggers reconciliation. The static ephemeris in spec.ntn
	// (ephemerisECEF or ephemerisOrbital) remains required.
	// +kubebuilder:validation:MinLength=1
	// +optional
	EphemerisRef string `json:"ephemerisRef,omitempty"`
}

// ProviderRef identifies the NTN backend provider.
type ProviderRef struct {
	// type is the provider type. Currently only "ocudu" is supported.
	// +kubebuilder:validation:Enum=ocudu
	Type string `json:"type"`

	// namespace where the provider resources (e.g., OCUDU gNB) are deployed.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// endpoint is the provider-specific endpoint (e.g., O1 NETCONF address).
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
}

// NTNParams defines the core NTN radio parameters.
// Exactly one of ephemerisECEF or ephemerisOrbital must be set.
// +kubebuilder:validation:XValidation:rule="has(self.ephemerisECEF) || has(self.ephemerisOrbital)",message="exactly one of ephemerisECEF or ephemerisOrbital must be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.ephemerisECEF) && has(self.ephemerisOrbital))",message="ephemerisECEF and ephemerisOrbital are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!has(self.ephemerisECEF) || self.ephemerisECEF.posX != 0 || self.ephemerisECEF.posY != 0 || self.ephemerisECEF.posZ != 0",message="ephemerisECEF position must not be all zeros"
type NTNParams struct {
	// cellSpecificKoffset sets the cell-specific k-offset for NTN (0-1023).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1023
	// +kubebuilder:default=150
	CellSpecificKoffset int `json:"cellSpecificKoffset,omitempty"`

	// taCommon sets the common Timing Advance value (0-66485757).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=66485757
	// +kubebuilder:default=0
	TACommon int `json:"taCommon,omitempty"`

	// ephemerisECEF defines the satellite position and velocity in ECEF coordinates.
	// Mutually exclusive with ephemerisOrbital.
	// +optional
	EphemerisECEF *EphemerisECEF `json:"ephemerisECEF,omitempty"`

	// ephemerisOrbital defines the satellite orbit using Keplerian elements.
	// Mutually exclusive with ephemerisECEF. Preferred for LEO satellites
	// where source data is in OMM/TLE form (CelesTrak, SpaceTrack).
	// +optional
	EphemerisOrbital *EphemerisOrbital `json:"ephemerisOrbital,omitempty"`

	// taInfo provides extended Timing Advance parameters per 3GPP TS 38.213.
	// When set, taInfo.taCommon takes precedence over the top-level taCommon field.
	// +optional
	TAInfo *TAInfo `json:"taInfo,omitempty"`

	// epochTime defines the SFN/subframe reference for NTN timing alignment.
	// +optional
	EpochTime *EpochTime `json:"epochTime,omitempty"`

	// feederLinkInfo provides feeder link parameters for Doppler compensation.
	// +optional
	FeederLinkInfo *FeederLinkInfo `json:"feederLinkInfo,omitempty"`

	// ntnGatewayLocation specifies the NTN gateway (ground station) coordinates.
	// +optional
	NTNGatewayLocation *NTNGatewayLocation `json:"ntnGatewayLocation,omitempty"`

	// movingRefLocation defines the Earth-moving reference location for LEO NTN cells.
	// 3GPP Release 18 SIB19 field. Used by UEs for timing/Doppler estimation.
	// +optional
	MovingRefLocation *MovingRefLocation `json:"movingRefLocation,omitempty"`

	// satSwitchWithResync provides satellite switch handover hints to UEs during
	// satellite-to-satellite transitions. 3GPP Release 18 SIB19 field.
	// +optional
	SatSwitchWithResync *SatSwitchWithResync `json:"satSwitchWithResync,omitempty"`

	// polarization specifies the antenna polarization for downlink and uplink.
	// Per 3GPP TS 38.331 SIB19, ntn-PolarizationDL-r17 and ntn-PolarizationUL-r17
	// are independent IEs. OCUDU collapses them under a single `polarization:` map
	// with `dl:` / `ul:` sub-keys, matching this CRD layout.
	// +optional
	Polarization *NTNPolarization `json:"polarization,omitempty"`

	// taReport enables UE TA reporting.
	// +optional
	TAReport *bool `json:"taReport,omitempty"`

	// ntnUlSyncValidityDur sets the UL synchronization validity duration in seconds.
	// +kubebuilder:validation:Enum=5;10;15;20;25;30;35;40;45;50;55;60;120;180;240;900
	// +optional
	NTNUlSyncValidityDur *int `json:"ntnUlSyncValidityDur,omitempty"`

	// payloadType specifies the satellite payload architecture.
	// +kubebuilder:validation:Enum=transparent;regenerative
	// +kubebuilder:default="transparent"
	PayloadType string `json:"payloadType,omitempty"`

	// neighborCells lists neighbor NTN cells for measurement/handover.
	// OCUDU YAML renders as "ncells:" for compatibility.
	// +optional
	NeighborCells []NTNNeighborCell `json:"neighborCells,omitempty"`

	// referenceLocation defines the NTN cell reference location.
	// +optional
	ReferenceLocation *ReferenceLocation `json:"referenceLocation,omitempty"`

	// distanceThreshold sets the distance threshold for cell
	// selection in metres.
	// +kubebuilder:validation:Minimum=0
	// +optional
	DistanceThreshold *int `json:"distanceThreshold,omitempty"`

	// tService sets the expected NTN service duration in seconds.
	// +kubebuilder:validation:Minimum=1
	// +optional
	TService *int `json:"tService,omitempty"`
}

// TAInfo provides extended Timing Advance parameters per 3GPP TS 38.213.
type TAInfo struct {
	// taCommon is the common Timing Advance value (0-66485757). Required when
	// taInfo is set — explicitly provide 0 for GEO satellites.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=66485757
	TACommon int `json:"taCommon"`

	// taCommonDrift is the TA drift rate.
	// +optional
	TACommonDrift int `json:"taCommonDrift,omitempty"`

	// taCommonDriftVariant is the TA drift rate variant.
	// +optional
	TACommonDriftVariant int `json:"taCommonDriftVariant,omitempty"`

	// taCommonOffset is an additional TA offset.
	// +optional
	TACommonOffset int `json:"taCommonOffset,omitempty"`
}

// EpochTime defines the SFN/subframe reference for NTN timing.
type EpochTime struct {
	// sfn is the System Frame Number (0-1023).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1023
	SFN int `json:"sfn"`

	// subframeNumber is the subframe within the SFN (0-9).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=9
	SubframeNumber int `json:"subframeNumber"`
}

// FeederLinkInfo provides feeder link parameters for Doppler compensation.
type FeederLinkInfo struct {
	// enableDopplerCompensation enables feeder link Doppler compensation.
	EnableDopplerCompensation bool `json:"enableDopplerCompensation"`

	// dlFreqHz is the downlink frequency in Hz. Required when feederLinkInfo is set.
	// +kubebuilder:validation:Minimum=1
	DLFreqHz int64 `json:"dlFreqHz"`

	// ulFreqHz is the uplink frequency in Hz. Required when feederLinkInfo is set.
	// +kubebuilder:validation:Minimum=1
	ULFreqHz int64 `json:"ulFreqHz"`
}

// NTNGatewayLocation specifies the NTN gateway coordinates.
// Values in 1e-4 degrees for latitude/longitude, metres for altitude.
type NTNGatewayLocation struct {
	// latitude in 1e-4 degrees (-900000 to 900000).
	// +kubebuilder:validation:Minimum=-900000
	// +kubebuilder:validation:Maximum=900000
	Latitude int `json:"latitude"`

	// longitude in 1e-4 degrees (-1800000 to 1800000).
	// +kubebuilder:validation:Minimum=-1800000
	// +kubebuilder:validation:Maximum=1800000
	Longitude int `json:"longitude"`

	// altitude in metres above sea level. Required when ntnGatewayLocation is set.
	Altitude int `json:"altitude"`
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

// NTNPolarization sets the antenna polarization for downlink and uplink
// independently, matching OCUDU's nested `polarization: { dl:, ul: }` YAML layout
// and 3GPP TS 38.331 `ntn-PolarizationDL-r17` / `ntn-PolarizationUL-r17`.
// At least one of `dl` / `ul` must be set when the parent is present.
// +kubebuilder:validation:XValidation:rule="has(self.dl) || has(self.ul)",message="at least one of dl or ul must be set"
type NTNPolarization struct {
	// dl is the downlink polarization broadcast in SIB19 ntn-PolarizationDL-r17.
	// +kubebuilder:validation:Enum=rhcp;lhcp;linear
	// +optional
	DL string `json:"dl,omitempty"`

	// ul is the uplink polarization broadcast in SIB19 ntn-PolarizationUL-r17.
	// +kubebuilder:validation:Enum=rhcp;lhcp;linear
	// +optional
	UL string `json:"ul,omitempty"`
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

// EphemerisOrbital defines the satellite orbit using Keplerian elements,
// matching OCUDU's orbital_coordinates_t representation.
// All angular values are in units of 1e-4 degrees (per 3GPP TS 38.331).
type EphemerisOrbital struct {
	// semiMajorAxis is the semi-major axis in metres.
	// +kubebuilder:validation:Minimum=6370000
	SemiMajorAxis int `json:"semiMajorAxis"`
	// eccentricity is the orbital eccentricity scaled by 1e6 (0-999999 for e < 1.0).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=999999
	Eccentricity int `json:"eccentricity"`
	// inclination is the orbital inclination in 1e-4 degrees (0-1800000 = 0°-180°).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1800000
	Inclination int `json:"inclination"`
	// rightAscension is the right ascension of the ascending node in 1e-4 degrees (0-3600000).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3600000
	RightAscension int `json:"rightAscension"`
	// argOfPeriapsis is the argument of periapsis in 1e-4 degrees (0-3600000).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3600000
	ArgOfPeriapsis int `json:"argOfPeriapsis"`
	// meanAnomaly is the mean anomaly in 1e-4 degrees (0-3600000).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3600000
	MeanAnomaly int `json:"meanAnomaly"`
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

// NTNNeighborCell describes a neighbor NTN cell.
type NTNNeighborCell struct {
	// physicalCellID of the neighbor (0-1007).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1007
	PhysicalCellID int `json:"physicalCellID"`

	// frequency is the neighbor cell's ARFCN (NR-ARFCN, always >= 1).
	// +kubebuilder:validation:Minimum=1
	// +optional
	Frequency int `json:"frequency,omitempty"`

	// reselectionInfo carries SIB11-style per-neighbor cell reselection
	// parameters per 3GPP TS 38.331 IntraFreqCellReselectionInfo. All
	// sub-fields are optional; unset fields are omitted from the rendered
	// config so OCUDU falls back to its internal defaults.
	// +optional
	ReselectionInfo *NeighborReselectionInfo `json:"reselectionInfo,omitempty"`
}

// NeighborReselectionInfo models the SIB11 per-neighbor reselection
// parameters that NTN operators tune for handover behavior.
type NeighborReselectionInfo struct {
	// qHyst is the reselection hysteresis in dB (TS 38.331 Q-Hyst enum:
	// 0,1,2,3,4,5,6,8,10,12,14,16,18,20,22,24).
	// +kubebuilder:validation:Enum=0;1;2;3;4;5;6;8;10;12;14;16;18;20;22;24
	// +optional
	QHyst *int `json:"qHyst,omitempty"`

	// qOffsetCell is the per-cell reselection offset in dB (TS 38.331
	// Q-OffsetRange, -24..24).
	// +kubebuilder:validation:Minimum=-24
	// +kubebuilder:validation:Maximum=24
	// +optional
	QOffsetCell *int `json:"qOffsetCell,omitempty"`

	// sIntraSearchP is the serving-cell RSRP threshold below which intra-frequency
	// measurements start (TS 38.331 ReselectionThreshold 0-31).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=31
	// +optional
	SIntraSearchP *int `json:"sIntraSearchP,omitempty"`

	// threshServingLowP is the serving-cell low threshold for inter-frequency
	// reselection (TS 38.331 ReselectionThreshold 0-31, in 2dB steps).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=31
	// +optional
	ThreshServingLowP *int `json:"threshServingLowP,omitempty"`
}

// ReferenceLocation defines the NTN cell reference location.
type ReferenceLocation struct {
	// latitude in 1e-4 degrees (-900000 to 900000).
	// +kubebuilder:validation:Minimum=-900000
	// +kubebuilder:validation:Maximum=900000
	Latitude int `json:"latitude"`

	// longitude in 1e-4 degrees (-1800000 to 1800000).
	// +kubebuilder:validation:Minimum=-1800000
	// +kubebuilder:validation:Maximum=1800000
	Longitude int `json:"longitude"`
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

	// sibSchedule tunes SIB19 broadcast scheduling. Any unset sub-field
	// falls back to the defaults (siWindowLength=5, siPeriod=16,
	// siWindowPosition=1). Tune when PDCCH capacity is tight or when
	// SIB19 broadcast cadence needs to track short ntn-UlSyncValidityDur.
	// +optional
	SIBSchedule *SIBSchedule `json:"sibSchedule,omitempty"`
}

// SIBSchedule tunes SIB19 scheduling parameters per 3GPP TS 38.331
// SI-SchedulingInfo. Enums follow OCUDU's CLI11 accepted values.
type SIBSchedule struct {
	// siWindowLength is the SI window length in slots. OCUDU accepts
	// the standard set; picking a larger value increases PDCCH pressure.
	// +kubebuilder:validation:Enum=5;10;20;40;80;160;320;640;1280
	// +optional
	SIWindowLength int `json:"siWindowLength,omitempty"`

	// siPeriod is the SIB19 broadcast period in radio frames.
	// Shorter periods keep UEs' NTN assistance fresh but cost air time.
	// +kubebuilder:validation:Enum=8;16;32;64;128;256;512
	// +optional
	SIPeriod int `json:"siPeriod,omitempty"`

	// siWindowPosition is the slot offset within the SI period. Adjust
	// to avoid collision with SIB1/SIB2 scheduling windows. Pointer so 0
	// (the first slot) can be distinguished from unset.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=79
	// +optional
	SIWindowPosition *int `json:"siWindowPosition,omitempty"`
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
// delegating configuration to the specified NTN backend provider.
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
