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

package ocudu

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
	"text/template"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
)

// OCUDU physical-unit conversion factors.
//
// The CRD carries 3GPP raw codepoints (the SIB19 IE representation), but OCUDU's
// gNB config parser expects PHYSICAL SI values (metres, m/s, radians, µs,
// degrees) and converts them back to codepoints internally for SIB19 ASN.1
// encoding. Emitting `codepoint × factor` therefore round-trips to exactly the
// intended SIB19 value. Factors are the inverse of OCUDU's own divisors, taken
// from lib/du/du_high/du_manager/converters/asn1_ntn_config_helpers.cpp:
//   ta_common / 0.004072, ta_common_drift / 0.0002, ta_common_drift_variant /
//   0.00002, position / 1.3, velocity / 0.06, eccentricity / 1.431e-8,
//   inclination|longitude|periapsis|mean_anomaly / 2.341e-8 (radians),
//   geodetic degrees. (ECEF position/velocity steps reuse the CRD's 3GPP
//   constants ntnv1alpha1.ECEFPositionStep / ECEFVelocityStep.)
const (
	// taCommonStepUs is µs per ta_common / ta_common_offset codepoint.
	taCommonStepUs = 0.004072
	// taCommonDriftStep is µs/s per ta_common_drift codepoint.
	taCommonDriftStep = 0.0002
	// taCommonDriftVarStep is µs/s² per ta_common_drift_variant codepoint.
	taCommonDriftVarStep = 0.00002
	// eccentricityScale converts the CRD's ×1e6-scaled eccentricity to the raw
	// dimensionless value OCUDU expects.
	eccentricityScale = 1.0e-6
	// oneEMinus4DegToDeg converts a 1e-4-degree codepoint to degrees (geodetic
	// latitude/longitude).
	oneEMinus4DegToDeg = 1.0e-4
)

// milliDegToRad converts a 1e-4-degree angular codepoint (orbital elements) to
// radians: (codepoint / 1e4) × π/180.
var milliDegToRad = math.Pi / 1_800_000.0

// flt formats a float64 as a plain decimal (never scientific notation, shortest
// round-tripping form) for emission into the OCUDU YAML config.
func flt(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// yamlQuote emits a YAML 1.2 double-quoted string using Go's strconv.Quote.
// strconv.Quote produces a Go-escaped double-quoted string; YAML 1.2
// double-quoted scalars accept these escapes, so newlines, colons, and other
// YAML-significant chars are escaped, preventing string injection from
// bleeding into the surrounding YAML structure even if apiserver enum
// validation is somehow bypassed (webhook misconfig, direct etcd write during
// migration, apiserver with --feature-gates=CRDValidation=false).
func yamlQuote(s string) string {
	return strconv.Quote(s)
}

// sanitizeComment strips characters that would terminate a YAML comment line
// (LF, CR, NEL, LS, PS, and NUL) so a user-supplied value cannot inject real
// YAML keys via the `# Payload type: ...` header line. Comments are
// line-terminated so quoting does not protect them.
func sanitizeComment(s string) string {
	r := strings.NewReplacer(
		"\n", " ",
		"\r", " ",
		"\u0085", " ",
		"\u2028", " ",
		"\u2029", " ",
		"\x00", "",
	)
	return r.Replace(s)
}

// configTemplate generates OCUDU-compatible NTN configuration YAML.
//
// Structure and key names are verified against the OCUDU gNB CLI11 config schema
// (apps/units/flexible_o_du/o_du_high/du_high/ntn/) and confirmed by booting the
// gNB. Key invariants (each previously wrong and rejected at parse time):
//   - the whole NTN block lives under cell_cfg.ntn; a top-level "ntn:" key is
//     rejected by the gNB (config_extras_mode::error)
//   - ta_common is under ntn.ta_info; ephemeris uses ephemeris_info_ecef |
//     ephemeris_orbital (variant)
//   - orbital elements use longitude/periapsis (NOT right_ascension/
//     arg_of_periapsis)
//   - neighbor cells render under "ncells:" with pci/carrier_freq (NOT
//     phys_cell_id/frequency)
//   - the feeder-link subcommand is "feeder_link" (NOT feeder_link_info)
//   - the serving-cell gateway is "gateway_location" (the "ntn_gateway_location"
//     key exists only nested under sat_switch_with_resync, not at cell_cfg.ntn)
//   - reference_location / moving_ref_location are latitude/longitude only
//   - si_sched_info schedules SIB2 ([2], schedulingInfoList) BEFORE SIB19
//     ([19], schedulingInfoList2 with si_window_position); a SIB19-only
//     schedule is rejected by OCUDU ("requires at least one SIB with ID < 15")
//
// NOTE: satSwitchWithResync is intentionally NOT emitted into the static YAML.
// It is a RUNTIME-only feature (pushed via the ntn_config_update remote command
// as the per-cell sat_switch_with_resync block — see wsclient.go), because that
// is the only OCUDU surface that accepts its k_mac field (issue #52) and because
// a boot-time "next satellite" is meaningless (the switch target is dynamic).
// The CRD field now mirrors OCUDU's structure but flows through the WebSocket
// push, never the ConfigMap.
const configTemplate = `# Generated by NTN K8s Operators — do not edit manually
# Payload type: {{ .PayloadType }} (3GPP Rel-19: transparent or regenerative)

cell_cfg:
  ntn:
    cell_specific_koffset: {{ .Koffset }}
    ta_info:
      ta_common: {{ flt .TACommon }}
{{- if .TAInfoExtended }}
      ta_common_drift: {{ flt .TACommonDrift }}
      ta_common_drift_variant: {{ flt .TACommonDriftVariant }}
      ta_common_offset: {{ flt .TACommonOffset }}
{{- end }}
{{- if .UseOrbital }}
    ephemeris_orbital:
      semi_major_axis: {{ .OrbSemiMajorAxis }}
      eccentricity: {{ flt .OrbEccentricity }}
      inclination: {{ flt .OrbInclination }}
      longitude: {{ flt .OrbRightAscension }}
      periapsis: {{ flt .OrbArgOfPeriapsis }}
      mean_anomaly: {{ flt .OrbMeanAnomaly }}
{{- else }}
    ephemeris_info_ecef:
      pos_x: {{ flt .EphPosX }}
      pos_y: {{ flt .EphPosY }}
      pos_z: {{ flt .EphPosZ }}
      vel_x: {{ flt .EphVelX }}
      vel_y: {{ flt .EphVelY }}
      vel_z: {{ flt .EphVelZ }}
{{- end }}
{{- if .EpochTime }}
    epoch_time:
      sfn: {{ .EpochSFN }}
      subframe_number: {{ .EpochSubframeNumber }}
{{- end }}
{{- if .FeederLink }}
    feeder_link:
      enable_doppler_compensation: {{ .FeederDopplerCompensation }}
      dl_freq: {{ .FeederDLFreq }}
      ul_freq: {{ .FeederULFreq }}
{{- end }}
{{- if .GatewayLocation }}
    gateway_location:
      latitude: {{ flt .GatewayLatitude }}
      longitude: {{ flt .GatewayLongitude }}
      altitude: {{ .GatewayAltitude }}
{{- end }}
{{- if .HasNCells }}
    ncells:
{{- range .NCells }}
      - pci: {{ .PhysicalCellID }}
{{- if .Frequency }}
        carrier_freq: {{ .Frequency }}
{{- end }}
{{- end }}
{{- end }}
{{- if .HasReferenceLocation }}
    reference_location:
      latitude: {{ flt .RefLatitude }}
      longitude: {{ flt .RefLongitude }}
{{- end }}
{{- if .HasDistanceThreshold }}
    distance_threshold: {{ .DistanceThreshold }}
{{- end }}
{{- if .MovingRefLocation }}
    moving_ref_location:
      latitude: {{ flt .MovingRefLatitude }}
      longitude: {{ flt .MovingRefLongitude }}
{{- end }}
{{- if .HasPolarization }}
    polarization:
{{- if .PolarizationDL }}
      dl: {{ yamlQuote .PolarizationDL }}
{{- end }}
{{- if .PolarizationUL }}
      ul: {{ yamlQuote .PolarizationUL }}
{{- end }}
{{- end }}
{{- if .TAReportSet }}
    ta_report: {{ .TAReport }}
{{- end }}
{{- if .UlSyncValidityDurSet }}
    ntn_ul_sync_validity_dur: {{ .UlSyncValidityDur }}
{{- end }}
  sib:
    si_window_length: {{ .SIWindowLength }}
    si_sched_info:
      # SIB2 (ID < 15) uses schedulingInfoList and must precede SIB19: OCUDU
      # rejects a SIB19-only schedule ("SIB19 requires at least one SIB with
      # ID < 15"). SIB2 broadcasts with OCUDU defaults (no sib2 block needed).
      - si_period: {{ .SIPeriod }}
        sib_mapping: [2]
      # SIB19 (ID >= 15) uses schedulingInfoList2 and needs an si_window_position
      # strictly greater than the number of preceding schedulingInfoList entries
      # (1 here), so si_window_position >= 2.
      - si_period: {{ .SIPeriod }}
        sib_mapping: [19]
        si_window_position: {{ .SIWindowPosition }}
  pdsch:
    max_nof_harq_retxs: {{ .PdschMaxHarqRetxs }}
  prach:
    max_msg3_harq_retx: {{ .PrachMaxMsg3HarqRetx }}

cu_cp:
  rrc:
    rrc_procedure_guard_time_ms: {{ .RrcGuardTimeMs }}
`

var parsedTemplate = template.Must(
	template.New("ocudu-ntn").
		Funcs(template.FuncMap{"yamlQuote": yamlQuote, "flt": flt}).
		Parse(configTemplate),
)

// ntnNeighborCellData holds per-neighbor data for template rendering.
type ntnNeighborCellData struct {
	PhysicalCellID int
	Frequency      int
}

type configData struct {
	PayloadType string
	// Koffset is the cell-specific K_offset in milliseconds. OCUDU stores
	// cell_specific_koffset as std::chrono::milliseconds and converts to
	// operating-SCS slots internally, so this value passes through unchanged
	// (no unit conversion). OCUDU accepts 1-1023 and rejects 0; the 3GPP IE
	// allows 0..1023, so the CRD's Minimum=1 mirrors OCUDU, not the spec.
	Koffset                   int
	// The following are PHYSICAL SI values emitted to OCUDU, converted from the
	// CRD's 3GPP codepoints (see the conversion-factor constants above).
	TACommon                  float64 // µs
	TAInfoExtended            bool
	TACommonDrift             float64 // µs/s
	TACommonDriftVariant      float64 // µs/s²
	TACommonOffset            float64 // µs
	UseOrbital                bool
	EphPosX                   float64 // m
	EphPosY                   float64 // m
	EphPosZ                   float64 // m
	EphVelX                   float64 // m/s
	EphVelY                   float64 // m/s
	EphVelZ                   float64 // m/s
	OrbSemiMajorAxis          int     // m (already physical in the CRD — passthrough)
	OrbEccentricity           float64 // dimensionless
	OrbInclination            float64 // rad
	OrbRightAscension         float64 // rad (OCUDU key: longitude)
	OrbArgOfPeriapsis         float64 // rad (OCUDU key: periapsis)
	OrbMeanAnomaly            float64 // rad
	EpochTime                 bool
	EpochSFN                  int
	EpochSubframeNumber       int
	FeederLink                bool
	FeederDopplerCompensation bool
	FeederDLFreq              int64
	FeederULFreq              int64
	GatewayLocation           bool
	GatewayLatitude           float64 // degrees
	GatewayLongitude          float64 // degrees
	GatewayAltitude           int     // m (passthrough)
	HasNCells                 bool
	NCells                    []ntnNeighborCellData
	HasReferenceLocation      bool
	RefLatitude               float64 // degrees
	RefLongitude              float64 // degrees
	HasDistanceThreshold      bool
	DistanceThreshold         int // m (passthrough)
	MovingRefLocation         bool
	MovingRefLatitude         float64 // degrees
	MovingRefLongitude        float64 // degrees
	HasPolarization           bool
	PolarizationDL            string
	PolarizationUL            string
	TAReportSet               bool
	TAReport                  bool
	UlSyncValidityDurSet      bool
	UlSyncValidityDur         int
	PdschMaxHarqRetxs         int
	PrachMaxMsg3HarqRetx      int
	RrcGuardTimeMs            int
	SIWindowLength            int
	SIPeriod                  int
	SIWindowPosition          int
}

// SI scheduling defaults mirror the live-gNB-verified geo_ntn.yml values.
const (
	defaultSIWindowLength   = 5
	defaultSIPeriod         = 16
	// defaultSIWindowPosition is 2: SIB19 (schedulingInfoList2) must sit strictly
	// after the single SIB2 (schedulingInfoList) entry the emitter always adds.
	defaultSIWindowPosition = 2
)

// GenerateConfig produces OCUDU-compatible NTN configuration YAML
// from an NTNCellConfigSpec. The output matches the CLI11 config format
// accepted by OCUDU gNB (CLI11 config format).
func GenerateConfig(spec *ntnv1alpha1.NTNCellConfigSpec) ([]byte, error) {
	if spec == nil {
		return nil, fmt.Errorf("spec must not be nil")
	}

	data := baseConfigData(spec)
	applyTAInfo(&data, spec)
	applyOptionalNTNFields(&data, spec)
	applyStage3Fields(&data, spec)
	applyCellOverrides(&data, spec)
	if err := applyEphemeris(&data, spec); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := parsedTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("executing config template: %w", err)
	}

	return buf.Bytes(), nil
}

func baseConfigData(spec *ntnv1alpha1.NTNCellConfigSpec) configData {
	payloadType := spec.NTN.PayloadType
	if payloadType == "" {
		payloadType = "transparent"
	}
	// Sanitize the comment-bound string so a malicious payload cannot break
	// out of the `# Payload type: ...` header into real YAML keys even if
	// apiserver enum validation were bypassed.
	payloadType = sanitizeComment(payloadType)

	return configData{
		PayloadType:          payloadType,
		Koffset:              spec.NTN.CellSpecificKoffset,
		TACommon:             float64(spec.NTN.TACommon) * taCommonStepUs,
		PdschMaxHarqRetxs:    0,
		PrachMaxMsg3HarqRetx: 0,
		RrcGuardTimeMs:       12800,
		SIWindowLength:       defaultSIWindowLength,
		SIPeriod:             defaultSIPeriod,
		SIWindowPosition:     defaultSIWindowPosition,
	}
}

func applyTAInfo(data *configData, spec *ntnv1alpha1.NTNCellConfigSpec) {
	if spec.NTN.TAInfo == nil {
		return
	}

	ta := spec.NTN.TAInfo
	data.TACommon = float64(ta.TACommon) * taCommonStepUs

	// Emit drift/offset sub-fields only when at least one is non-zero.
	if ta.TACommonDrift != 0 || ta.TACommonDriftVariant != 0 || ta.TACommonOffset != 0 {
		data.TAInfoExtended = true
		data.TACommonDrift = float64(ta.TACommonDrift) * taCommonDriftStep
		data.TACommonDriftVariant = float64(ta.TACommonDriftVariant) * taCommonDriftVarStep
		data.TACommonOffset = float64(ta.TACommonOffset) * taCommonStepUs
	}
}

func applyOptionalNTNFields(data *configData, spec *ntnv1alpha1.NTNCellConfigSpec) {
	if spec.NTN.EpochTime != nil {
		data.EpochTime = true
		data.EpochSFN = spec.NTN.EpochTime.SFN
		data.EpochSubframeNumber = spec.NTN.EpochTime.SubframeNumber
	}
	if spec.NTN.FeederLinkInfo != nil {
		data.FeederLink = true
		data.FeederDopplerCompensation = spec.NTN.FeederLinkInfo.EnableDopplerCompensation
		data.FeederDLFreq = spec.NTN.FeederLinkInfo.DLFreqHz
		data.FeederULFreq = spec.NTN.FeederLinkInfo.ULFreqHz
	}
	if spec.NTN.NTNGatewayLocation != nil {
		data.GatewayLocation = true
		data.GatewayLatitude = float64(spec.NTN.NTNGatewayLocation.Latitude) * oneEMinus4DegToDeg
		data.GatewayLongitude = float64(spec.NTN.NTNGatewayLocation.Longitude) * oneEMinus4DegToDeg
		data.GatewayAltitude = spec.NTN.NTNGatewayLocation.Altitude
	}
	if spec.NTN.MovingRefLocation != nil {
		data.MovingRefLocation = true
		data.MovingRefLatitude = float64(spec.NTN.MovingRefLocation.Latitude) * oneEMinus4DegToDeg
		data.MovingRefLongitude = float64(spec.NTN.MovingRefLocation.Longitude) * oneEMinus4DegToDeg
	}
	// satSwitchWithResync is intentionally not emitted into static YAML — see the
	// configTemplate note. It is delivered at runtime via the ntn_config_update
	// remote command (wsclient.go), the only surface carrying its k_mac field.
	if spec.NTN.Polarization != nil {
		pol := spec.NTN.Polarization
		if pol.DL != "" || pol.UL != "" {
			data.HasPolarization = true
			data.PolarizationDL = pol.DL
			data.PolarizationUL = pol.UL
		}
	}
	if spec.NTN.TAReport != nil {
		data.TAReportSet = true
		data.TAReport = *spec.NTN.TAReport
	}
	if spec.NTN.NTNUlSyncValidityDur != nil {
		data.UlSyncValidityDurSet = true
		data.UlSyncValidityDur = *spec.NTN.NTNUlSyncValidityDur
	}
}

func applyStage3Fields(data *configData, spec *ntnv1alpha1.NTNCellConfigSpec) {
	if len(spec.NTN.NeighborCells) > 0 {
		data.HasNCells = true
		for _, nc := range spec.NTN.NeighborCells {
			data.NCells = append(data.NCells, ntnNeighborCellData{
				PhysicalCellID: nc.PhysicalCellID,
				Frequency:      nc.Frequency,
			})
		}
	}
	if spec.NTN.ReferenceLocation != nil {
		data.HasReferenceLocation = true
		data.RefLatitude = float64(spec.NTN.ReferenceLocation.Latitude) * oneEMinus4DegToDeg
		data.RefLongitude = float64(spec.NTN.ReferenceLocation.Longitude) * oneEMinus4DegToDeg
	}
	if spec.NTN.DistanceThreshold != nil {
		data.HasDistanceThreshold = true
		data.DistanceThreshold = *spec.NTN.DistanceThreshold
	}
	// t_service is intentionally NOT emitted: OCUDU's t_service is an ABSOLUTE
	// end-of-service time (Unix ms / UTC string), but the CRD models tService as
	// a service DURATION in seconds. Emitting a duration as a timestamp would be
	// wrong (e.g. 100 → 100 ms past the 1970 epoch). Correct support needs the
	// CRD to carry an absolute time; tracked with the sat-switch redesign work.
}

func applyCellOverrides(data *configData, spec *ntnv1alpha1.NTNCellConfigSpec) {
	if spec.CellOverrides == nil {
		return
	}

	if spec.CellOverrides.PdschMaxHarqRetxs > 0 {
		data.PdschMaxHarqRetxs = spec.CellOverrides.PdschMaxHarqRetxs
	}
	if spec.CellOverrides.PrachMaxMsg3HarqRetx > 0 {
		data.PrachMaxMsg3HarqRetx = spec.CellOverrides.PrachMaxMsg3HarqRetx
	}
	if spec.CellOverrides.RrcGuardTimeMs > 0 {
		data.RrcGuardTimeMs = spec.CellOverrides.RrcGuardTimeMs
	}
	if sched := spec.CellOverrides.SIBSchedule; sched != nil {
		if sched.SIWindowLength > 0 {
			data.SIWindowLength = sched.SIWindowLength
		}
		if sched.SIPeriod > 0 {
			data.SIPeriod = sched.SIPeriod
		}
		if sched.SIWindowPosition != nil {
			data.SIWindowPosition = *sched.SIWindowPosition
		}
	}
}

func applyEphemeris(data *configData, spec *ntnv1alpha1.NTNCellConfigSpec) error {
	if spec.NTN.EphemerisECEF != nil && spec.NTN.EphemerisOrbital != nil {
		return fmt.Errorf("ephemerisECEF and ephemerisOrbital are mutually exclusive")
	}

	switch {
	case spec.NTN.EphemerisOrbital != nil:
		orb := spec.NTN.EphemerisOrbital
		data.UseOrbital = true
		data.OrbSemiMajorAxis = orb.SemiMajorAxis // metres — already physical, passthrough
		data.OrbEccentricity = float64(orb.Eccentricity) * eccentricityScale
		data.OrbInclination = float64(orb.Inclination) * milliDegToRad
		data.OrbRightAscension = float64(orb.RightAscension) * milliDegToRad
		data.OrbArgOfPeriapsis = float64(orb.ArgOfPeriapsis) * milliDegToRad
		data.OrbMeanAnomaly = float64(orb.MeanAnomaly) * milliDegToRad
	case spec.NTN.EphemerisECEF != nil:
		e := spec.NTN.EphemerisECEF
		data.EphPosX = float64(e.PosX) * ntnv1alpha1.ECEFPositionStep
		data.EphPosY = float64(e.PosY) * ntnv1alpha1.ECEFPositionStep
		data.EphPosZ = float64(e.PosZ) * ntnv1alpha1.ECEFPositionStep
		data.EphVelX = float64(e.VelX) * ntnv1alpha1.ECEFVelocityStep
		data.EphVelY = float64(e.VelY) * ntnv1alpha1.ECEFVelocityStep
		data.EphVelZ = float64(e.VelZ) * ntnv1alpha1.ECEFVelocityStep
	default:
		return fmt.Errorf("either ephemerisECEF or ephemerisOrbital must be set")
	}

	return nil
}
