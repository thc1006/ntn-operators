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

// NTNCellConfigSpec defines the desired NTN cell configuration.
// +kubebuilder:validation:XValidation:rule="!has(self.provider.remoteControl) || has(self.cellID)",message="cellID is required when provider.remoteControl is set (runtime push targets a cell by plmn+nci)"
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
	// referenced SatelliteEphemeris is updated and invokes runtime ephemeris push
	// on the provider reconcile path. The static ephemeris in spec.ntn
	// (ephemerisECEF or ephemerisOrbital) remains required as the source payload.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +optional
	EphemerisRef string `json:"ephemerisRef,omitempty"`

	// cellID identifies the OCUDU cell (plmn + nci) that runtime remote commands
	// target. Required when provider.remoteControl is set; the value must match
	// the cell the gNB booted with. Unset ⇒ ConfigMap bootstrap path only.
	// +optional
	CellID *CellID `json:"cellID,omitempty"`

	// ephemerisNoradID selects which satellite's propagated state vector, from the
	// referenced SatelliteEphemeris (ephemerisRef), to push at runtime (#176). When
	// unset, the referenced ephemeris's first propagated state is used.
	// +optional
	EphemerisNoradID *int `json:"ephemerisNoradID,omitempty"`
}

// CellID identifies an OCUDU cell for runtime remote commands (ntn_config_update,
// cell_lock/unlock). It must match the cell the gNB booted with.
type CellID struct {
	// plmn is the cell's PLMN, 5 or 6 digits (e.g. "00101").
	// +kubebuilder:validation:Pattern=`^[0-9]{5,6}$`
	PLMN string `json:"plmn"`
	// nci is the 36-bit NR Cell Identity (0 to 2^36-1).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=68719476735
	NCI int64 `json:"nci"`
}

// RemoteControlRef configures the gNB remote_control WebSocket for live NTN
// config push (OCUDU ntn_config_update). OCUDU's remote_control server is
// plaintext/unauthenticated (localhost by default), so the safe deployment is a
// sidecar next to the gNB pod; cross-pod requires a NetworkPolicy.
type RemoteControlRef struct {
	// endpoint is host:port of the gNB remote_control server — a hostname/IPv4
	// ("127.0.0.1:8001") or a bracketed IPv6 literal ("[::1]:8001") for dual-stack
	// clusters. The provider prepends ws://, so include NEITHER a scheme nor a path
	// — a value like "ws://host:8001" would dial "ws://ws://host:8001" and fail.
	// Validation is layered: the pattern enforces the bare host:port shape, and CEL
	// rules enforce the port range (1-65535) and that a bracketed host is a real IP
	// (a permanent admission error beats a silent tight-requeue on a mistyped value).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=261
	// +kubebuilder:validation:Pattern=`^(\[[0-9a-fA-F:]+\]|[a-zA-Z0-9._-]+):[0-9]{1,5}$`
	// +kubebuilder:validation:XValidation:rule="int(self.substring(self.lastIndexOf(':') + 1)) >= 1 && int(self.substring(self.lastIndexOf(':') + 1)) <= 65535",message="endpoint port must be between 1 and 65535"
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('[') || isIP(self.substring(1, self.lastIndexOf(']')))",message="a bracketed endpoint host must be a valid IP address"
	Endpoint string `json:"endpoint"`

	// tls, when set, secures the runtime push: the provider dials wss:// (TLS)
	// instead of plaintext ws:// and authenticates with the material in the
	// referenced Secret. Omit it to keep the plaintext ws:// behavior (N-12).
	// +optional
	TLS *RemoteControlTLS `json:"tls,omitempty"`
}

// RemoteControlTLS configures TLS (wss://) and shared-secret / mTLS authentication
// for the runtime gNB config-update push (N-12). The trust and credential material
// lives in a Kubernetes Secret in the NTNCellConfig's OWN namespace — never inline
// in the CR — referenced by name.
type RemoteControlTLS struct {
	// mode selects the transport-security posture:
	//   "tls"  — dial wss://, verify the server certificate, and (if the Secret
	//            carries a token) send it as an Authorization: Bearer header.
	//   "mtls" — additionally present a client certificate (mutual TLS); the
	//            Secret MUST then carry tls.crt + tls.key.
	// +kubebuilder:validation:Enum=tls;mtls
	Mode string `json:"mode"`

	// secretName is the Secret (in this NTNCellConfig's namespace) holding the TLS
	// trust and auth material. Recognized keys: "ca.crt" (PEM CA to verify the
	// gNB/proxy server certificate — omit to use the system roots), "token" (the
	// shared secret sent as Authorization: Bearer — optional), and, for mode=mtls,
	// "tls.crt" + "tls.key" (the client certificate/key). A bare shared secret is
	// replayable, so it is only ever sent over the wss:// (TLS) connection.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	SecretName string `json:"secretName"`

	// serverName overrides the TLS ServerName (SNI) verified against the server
	// certificate's SubjectAltNames. Defaults to the endpoint host. Set it when the
	// gNB/proxy certificate's SAN does not match the dialed host (e.g. an IP
	// endpoint fronted by a DNS-named certificate).
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ServerName string `json:"serverName,omitempty"`
}

// ProviderRef identifies the NTN backend provider.
type ProviderRef struct {
	// type is the provider type. Currently only "ocudu" is supported.
	// +kubebuilder:validation:Enum=ocudu
	Type string `json:"type"`

	// namespace where the provider resources (e.g., OCUDU gNB) are deployed.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace,omitempty"`

	// remoteControl configures the gNB remote_control WebSocket for live NTN
	// config push. When set together with spec.cellID, the operator pushes runtime
	// ntn_config_update commands; otherwise it uses the ConfigMap path only.
	// +optional
	RemoteControl *RemoteControlRef `json:"remoteControl,omitempty"`

	// endpoint is the provider-specific endpoint (e.g., O1 NETCONF address).
	// +optional
	// +kubebuilder:validation:MaxLength=261
	Endpoint string `json:"endpoint,omitempty"`
}

// NTNParams defines the core NTN radio parameters.
// Exactly one of ephemerisECEF or ephemerisOrbital must be set.
// +kubebuilder:validation:XValidation:rule="has(self.ephemerisECEF) || has(self.ephemerisOrbital)",message="exactly one of ephemerisECEF or ephemerisOrbital must be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.ephemerisECEF) && has(self.ephemerisOrbital))",message="ephemerisECEF and ephemerisOrbital are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!has(self.ephemerisECEF) || self.ephemerisECEF.posX != 0 || self.ephemerisECEF.posY != 0 || self.ephemerisECEF.posZ != 0",message="ephemerisECEF position must not be all zeros"
type NTNParams struct {
	// cellSpecificKoffset is the cell-specific K_offset for NTN scheduling timing
	// (3GPP TS 38.213 / TS 38.300 §16.14.2). Its unit is milliseconds: OCUDU stores
	// cell_specific_koffset as std::chrono::milliseconds and converts it to
	// operating-SCS slots internally, so the value passes through this operator
	// unchanged with no unit conversion. (3GPP expresses K_offset as a slot count
	// assuming the 15 kHz reference SCS, where 1 slot = 1 ms; that identity is only
	// how the IE is defined, not a conversion the user applies here.)
	// The 3GPP IE cellSpecificKoffset-r17 is INTEGER(0..1023), but OCUDU rejects 0
	// (its CLI and config validation enforce 1-1023), so Minimum is 1 to mirror the
	// backend rather than the spec.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1023
	// +kubebuilder:default=150
	CellSpecificKoffset int `json:"cellSpecificKoffset,omitempty"`

	// taCommon sets the common Timing Advance value (0-66485757).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=66485756
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
	// +kubebuilder:validation:Maximum=66485756
	TACommon int `json:"taCommon"`

	// taCommonDrift is the TA drift rate in 3GPP codepoints (ta-CommonDrift-r17,
	// -257303 to 257303). The operator emits taCommonDrift × 2e-4 as µs/s
	// (OCUDU accepts ±51.4606 µs/s).
	// +kubebuilder:validation:Minimum=-257303
	// +kubebuilder:validation:Maximum=257303
	// +optional
	TACommonDrift int `json:"taCommonDrift,omitempty"`

	// taCommonDriftVariant is the TA drift-rate variant in codepoints
	// (ta-CommonDriftVariant-r17, 0 to 28949). Emitted × 2e-5 as µs/s²
	// (OCUDU accepts 0-0.57898 µs/s²).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=28949
	// +optional
	TACommonDriftVariant int `json:"taCommonDriftVariant,omitempty"`

	// taCommonOffset is an additional common-TA offset in codepoints (same
	// 0.004072 µs granularity as taCommon; 0 to 2455796 maps to OCUDU's 0-10000 µs).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2455795
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

// SatSwitchWithResync describes a scheduled satellite-to-satellite switch
// (3GPP Release 18 SIB19 sat-SwitchWithReSync), mirroring OCUDU's
// sat_switch_with_resync_t. It carries the NEXT satellite's assistance info so
// the cell can pre-announce the switch before the serving satellite leaves
// coverage.
//
// Delivery is RUNTIME-ONLY: the operator pushes it via OCUDU's ntn_config_update
// remote command as the per-cell `sat_switch_with_resync` block. That is the one
// OCUDU surface that accepts k_mac (issue #52) — OCUDU's bootstrap YAML exposes
// no k_mac handle on any cell, so this field is never emitted into the static
// ConfigMap (a boot-time "next satellite" would also be meaningless, the switch
// target is inherently dynamic). Requires spec.provider.remoteControl +
// spec.cellID; without them the field is inert.
//
// This is the switch MECHANISM only. Deciding WHEN to switch (autonomous
// multi-satellite failover) is issue #49 and remains out of scope here — the
// operator pushes whatever switch the spec declares.
type SatSwitchWithResync struct {
	// ntnConfig is the target satellite's NTN configuration after the switch.
	// Required: OCUDU rejects a sat_switch_with_resync that has no ntn_cfg.
	NTNConfig SatSwitchNTNConfig `json:"ntnConfig"`

	// epochUnixMs is the reference epoch for the target assistance info, in Unix
	// milliseconds. 0 omits the field. Unlike the serving-cell epoch, OCUDU does
	// not require the sat-switch epoch to be in the future.
	// +optional
	EpochUnixMs int64 `json:"epochUnixMs,omitempty"`

	// tServiceStartUnixMs is when the target satellite starts serving, in Unix
	// milliseconds. 0 omits the field.
	// +optional
	TServiceStartUnixMs int64 `json:"tServiceStartUnixMs,omitempty"`

	// ssbTimeOffsetSubframes is the SSB time offset in subframes (0-159), mapping
	// to OCUDU's ssb_time_offset_sf.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=159
	// +optional
	SSBTimeOffsetSubframes int `json:"ssbTimeOffsetSubframes,omitempty"`

	// gatewayLocation is the target satellite's NTN gateway (feeder) location,
	// emitted as OCUDU's ntn_gateway_location geodetic coordinates.
	// +optional
	GatewayLocation *NTNGatewayLocation `json:"gatewayLocation,omitempty"`
}

// SatSwitchNTNConfig is the target satellite's assistance info carried inside a
// satellite switch (OCUDU sat_switch_with_resync.ntn_cfg). Exactly one ephemeris
// representation must be set — switching to a satellite with no ephemeris is
// meaningless.
// +kubebuilder:validation:XValidation:rule="has(self.ephemerisECEF) || has(self.ephemerisOrbital)",message="exactly one of ephemerisECEF or ephemerisOrbital must be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.ephemerisECEF) && has(self.ephemerisOrbital))",message="ephemerisECEF and ephemerisOrbital are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!has(self.ephemerisECEF) || self.ephemerisECEF.posX != 0 || self.ephemerisECEF.posY != 0 || self.ephemerisECEF.posZ != 0",message="ephemerisECEF position must not be all zeros"
type SatSwitchNTNConfig struct {
	// ephemerisECEF is the target satellite's ECEF state vector.
	// Mutually exclusive with ephemerisOrbital.
	// +optional
	EphemerisECEF *EphemerisECEF `json:"ephemerisECEF,omitempty"`

	// ephemerisOrbital is the target satellite's Keplerian elements.
	// Mutually exclusive with ephemerisECEF.
	// +optional
	EphemerisOrbital *EphemerisOrbital `json:"ephemerisOrbital,omitempty"`

	// kMac is the MAC-CE scheduling offset k_mac (3GPP kmac-r17, INTEGER 1..512).
	// It tunes the k-offset applied to MAC CE contention-based resolution so UE
	// MAC feedback stays time-aligned with the satellite round-trip; distinct from
	// cellSpecificKoffset (PUSCH/PDSCH offset).
	//
	// This is the ONLY OCUDU surface that accepts k_mac (issue #52): the runtime
	// ntn_config_update command's sat_switch_with_resync.ntn_cfg. k_mac is NOT in
	// OCUDU's bootstrap YAML on any cell (serving, neighbor, or satswitch), so the
	// serving-cell static config remains 7/8 on the ntn_config field set by design.
	//
	// The 1..512 bound here is LOAD-BEARING: OCUDU's runtime update path does not
	// range-check k_mac (verified against a live gNB — it accepts out-of-range
	// values that would then violate the ASN.1 kmac-r17 constraint), so this CRD
	// validation is the only guard keeping a 3GPP-invalid k_mac off the wire.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=512
	// +optional
	KMac *int `json:"kMac,omitempty"`

	// cellSpecificKoffset is the target cell-specific K_offset in milliseconds,
	// same semantics as NTNParams.cellSpecificKoffset: 1-1023, with Minimum 1
	// because OCUDU rejects 0. Omit the field to leave it unset.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1023
	// +optional
	CellSpecificKoffset int `json:"cellSpecificKoffset,omitempty"`

	// ntnUlSyncValidityDur is the target UL-sync validity duration in seconds.
	// OCUDU keys this ntn_ul_sync_validity_dur inside ntn_cfg — deliberately
	// distinct from the serving-cell ntn_ul_sync_validity_duration key.
	// +kubebuilder:validation:Enum=5;10;15;20;25;30;35;40;45;50;55;60;120;180;240;900
	// +optional
	NTNUlSyncValidityDur *int `json:"ntnUlSyncValidityDur,omitempty"`

	// taInfo provides the target satellite's timing-advance parameters. The
	// runtime ntn_cfg accepts only ta_common / ta_common_drift /
	// ta_common_drift_variant — NOT ta_common_offset (that key is YAML-only), so
	// taInfo.taCommonOffset is ignored when pushed here.
	// +optional
	TAInfo *TAInfo `json:"taInfo,omitempty"`

	// taReport enables UE TA reporting for the target satellite.
	// +optional
	TAReport *bool `json:"taReport,omitempty"`
}

// EphemerisOrbital defines the satellite orbit using Keplerian elements,
// matching OCUDU's orbital_coordinates_t representation.
// All angular values are in units of 1e-4 degrees (per 3GPP TS 38.331).
type EphemerisOrbital struct {
	// semiMajorAxis is the semi-major axis in metres, emitted as-is to OCUDU
	// (which accepts 6500000-42998632 m).
	// +kubebuilder:validation:Minimum=6500000
	// +kubebuilder:validation:Maximum=42998632
	SemiMajorAxis int `json:"semiMajorAxis"`
	// eccentricity is the orbital eccentricity scaled by 1e6 (0-15005). The
	// operator emits eccentricity × 1e-6; OCUDU accepts e ≤ 0.01500510825.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=15005
	Eccentricity int `json:"eccentricity"`
	// inclination is the orbital inclination in 1e-4 degrees; the operator emits
	// inclination × π/1.8e6 as radians. OCUDU's orbital ephemeris accepts only
	// [0°, 90°] (0 to +π/2 rad), so inclinations above 90° (e.g. sun-synchronous
	// ~98°, retrograde) are NOT representable via the orbital path — use
	// ephemerisECEF (SGP4 state vector) for those. Max is 900000 (90°).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=900000
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

// 3GPP TS 38.331 ECEF scaling and range constants for ntn-Config-r17.
const (
	// ECEFPositionStep is the position quantization step in metres (1.3 m per LSB).
	ECEFPositionStep = 1.3
	// ECEFVelocityStep is the velocity quantization step in m/s (0.06 m/s per LSB).
	ECEFVelocityStep = 0.06
	// ECEFPosMin is the 3GPP positionX/Y/Z-r17 lower bound -2^25; × ECEFPositionStep
	// (1.3 m) = -43620761.6 m, exactly OCUDU's accepted floor.
	ECEFPosMin = -33554432
	// ECEFPosMax is 33554430, one below the 3GPP upper bound 2^25-1 (33554431):
	// OCUDU's CLI range tops out at 43620759.3 m = 33554430.2 codepoints, so
	// 33554431 (→ 43620760.3 m) would be rejected. (Previously this was ±2^26,
	// twice the spec, which let the emitter produce out-of-range metres.)
	ECEFPosMax = 33554430
	// ECEFVelMax/ECEFVelMin are the 3GPP velocityVX/VY/VZ-r17 bounds,
	// INTEGER(-2^17 .. 2^17-1). × ECEFVelocityStep (0.06 m/s) this maps to
	// OCUDU's accepted physical range ±7864.32 m/s.
	ECEFVelMax = 131071
	ECEFVelMin = -131072
)

// EphemerisECEF defines the satellite position and velocity in Earth-Centered
// Earth-Fixed coordinates. For GEO satellites, velocity fields should be 0.
type EphemerisECEF struct {
	// posX is the X position in 1.3 m/LSB codepoints (3GPP positionX-r17,
	// -33554432 to 33554431). The operator emits posX × 1.3 as metres to OCUDU.
	// +kubebuilder:validation:Minimum=-33554432
	// +kubebuilder:validation:Maximum=33554430
	PosX int `json:"posX"`
	// posY is the Y position in 1.3 m/LSB codepoints (-33554432 to 33554431).
	// +kubebuilder:validation:Minimum=-33554432
	// +kubebuilder:validation:Maximum=33554430
	PosY int `json:"posY"`
	// posZ is the Z position in 1.3 m/LSB codepoints (-33554432 to 33554431).
	// +kubebuilder:validation:Minimum=-33554432
	// +kubebuilder:validation:Maximum=33554430
	PosZ int `json:"posZ"`
	// velX is the X velocity in 0.06 m/s/LSB codepoints (3GPP velocityVX-r17,
	// -131072 to 131071; 0 for GEO). The operator emits velX × 0.06 as m/s.
	// +kubebuilder:validation:Minimum=-131072
	// +kubebuilder:validation:Maximum=131071
	// +kubebuilder:default=0
	VelX int `json:"velX,omitempty"`
	// velY is the Y velocity in 0.06 m/s/LSB codepoints (-131072 to 131071; 0 for GEO).
	// +kubebuilder:validation:Minimum=-131072
	// +kubebuilder:validation:Maximum=131071
	// +kubebuilder:default=0
	VelY int `json:"velY,omitempty"`
	// velZ is the Z velocity in 0.06 m/s/LSB codepoints (-131072 to 131071; 0 for GEO).
	// +kubebuilder:validation:Minimum=-131072
	// +kubebuilder:validation:Maximum=131071
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
	// siWindowPosition=2). Tune when PDCCH capacity is tight or when
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

	// siWindowPosition is SIB19's slot offset within the SI period
	// (schedulingInfoList2-r17). It must be strictly greater than the number of
	// preceding schedulingInfoList entries; the emitter always schedules one SIB2
	// (ID < 15) before SIB19, so the minimum is 2. Pointer so an explicit value is
	// distinguished from unset (which defaults to 2).
	// +kubebuilder:validation:Minimum=2
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
	ConditionConfigApplied   = "ConfigApplied"
	ConditionConfigValid     = "ConfigValid"
	ConditionEphemerisPushed = "EphemerisPushed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ntncc
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider.type`
// +kubebuilder:printcolumn:name="Koffset",type=integer,JSONPath=`.spec.ntn.cellSpecificKoffset`
// +kubebuilder:printcolumn:name="Payload",type=string,JSONPath=`.spec.ntn.payloadType`
// +kubebuilder:printcolumn:name="Applied",type=string,JSONPath=`.status.conditions[?(@.type=="ConfigApplied")].status`
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
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &NTNCellConfig{}, &NTNCellConfigList{})
		return nil
	})
}
