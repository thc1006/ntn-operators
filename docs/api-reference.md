# API Reference

Packages:

- [ntn.operators.dev/v1alpha1](#ntnoperatorsdevv1alpha1)

# ntn.operators.dev/v1alpha1

Resource Types:

- [GroundStationLifecycle](#groundstationlifecycle)

- [NTNCellConfig](#ntncellconfig)

- [NTNSlice](#ntnslice)

- [SatelliteEphemeris](#satelliteephemeris)




## GroundStationLifecycle
<sup><sup>[↩ Parent](#ntnoperatorsdevv1alpha1 )</sup></sup>






GroundStationLifecycle manages the lifecycle of a satellite ground station,
including health monitoring, firmware OTA, and GitOps-based configuration.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
      <td><b>apiVersion</b></td>
      <td>string</td>
      <td>ntn.operators.dev/v1alpha1</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b>kind</b></td>
      <td>string</td>
      <td>GroundStationLifecycle</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta">metadata</a></b></td>
      <td>object</td>
      <td>Refer to the Kubernetes API documentation for the fields of the `metadata` field.</td>
      <td>true</td>
      </tr><tr>
        <td><b><a href="#groundstationlifecyclespec">spec</a></b></td>
        <td>object</td>
        <td>
          GroundStationLifecycleSpec defines the desired state of a ground station.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#groundstationlifecyclestatus">status</a></b></td>
        <td>object</td>
        <td>
          GroundStationLifecycleStatus defines the observed state of a ground station.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### GroundStationLifecycle.spec
<sup><sup>[↩ Parent](#groundstationlifecycle)</sup></sup>



GroundStationLifecycleSpec defines the desired state of a ground station.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#groundstationlifecyclespecdeployment">deployment</a></b></td>
        <td>object</td>
        <td>
          deployment defines the station deployment and location.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#groundstationlifecyclespechardware">hardware</a></b></td>
        <td>object</td>
        <td>
          hardware describes the ground station equipment.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#groundstationlifecyclespecfirmware">firmware</a></b></td>
        <td>object</td>
        <td>
          firmware defines OTA update configuration.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#groundstationlifecyclespecmonitoring">monitoring</a></b></td>
        <td>object</td>
        <td>
          monitoring defines health check parameters.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### GroundStationLifecycle.spec.deployment
<sup><sup>[↩ Parent](#groundstationlifecyclespec)</sup></sup>



deployment defines the station deployment and location.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#groundstationlifecyclespecdeploymentlocation">location</a></b></td>
        <td>object</td>
        <td>
          location is the geographic position of the ground station.<br/>
          <br/>
            <i>Validations</i>:<li>double(self.lat) >= -90.0 && double(self.lat) <= 90.0: lat must be between -90 and 90</li><li>double(self.lon) >= -180.0 && double(self.lon) <= 180.0: lon must be between -180 and 180</li>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>gitopsRepo</b></td>
        <td>string</td>
        <td>
          gitopsRepo is the Git repository URL for GitOps-managed configuration.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>k8sDistro</b></td>
        <td>enum</td>
        <td>
          k8sDistro is the Kubernetes distribution running on the edge box.<br/>
          <br/>
            <i>Enum</i>: k3s, microk8s, rke2<br/>
            <i>Default</i>: k3s<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### GroundStationLifecycle.spec.deployment.location
<sup><sup>[↩ Parent](#groundstationlifecyclespecdeployment)</sup></sup>



location is the geographic position of the ground station.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>lat</b></td>
        <td>string</td>
        <td>
          lat is the latitude in decimal degrees (string, e.g., "25.0330").<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>lon</b></td>
        <td>string</td>
        <td>
          lon is the longitude in decimal degrees (string, e.g., "121.5654").<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>alt</b></td>
        <td>string</td>
        <td>
          alt is the altitude in meters above sea level (string, e.g., "15").<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### GroundStationLifecycle.spec.hardware
<sup><sup>[↩ Parent](#groundstationlifecyclespec)</sup></sup>



hardware describes the ground station equipment.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>model</b></td>
        <td>string</td>
        <td>
          model is the hardware model identifier.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>vendor</b></td>
        <td>string</td>
        <td>
          vendor is the hardware manufacturer (e.g., "ennoconn").<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>antennaType</b></td>
        <td>string</td>
        <td>
          antennaType is the antenna type (e.g., "flat-panel", "parabolic").<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>bands</b></td>
        <td>[]string</td>
        <td>
          bands lists the supported frequency bands (e.g., ["Ka", "Ku", "S"]).<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### GroundStationLifecycle.spec.firmware
<sup><sup>[↩ Parent](#groundstationlifecyclespec)</sup></sup>



firmware defines OTA update configuration.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>autoUpdate</b></td>
        <td>boolean</td>
        <td>
          autoUpdate enables automatic firmware updates.<br/>
          <br/>
            <i>Default</i>: false<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>channel</b></td>
        <td>string</td>
        <td>
          channel is the firmware update channel (e.g., "stable", "beta").<br/>
          <br/>
            <i>Default</i>: stable<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>maintenanceWindow</b></td>
        <td>string</td>
        <td>
          maintenanceWindow is the time window for updates.
Format: "HH:MM-HH:MM UTC" (e.g., "02:00-04:00 UTC").<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### GroundStationLifecycle.spec.monitoring
<sup><sup>[↩ Parent](#groundstationlifecyclespec)</sup></sup>



monitoring defines health check parameters.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>endpoint</b></td>
        <td>string</td>
        <td>
          endpoint is the monitoring endpoint of the ground station agent.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>healthCheckInterval</b></td>
        <td>string</td>
        <td>
          healthCheckInterval is how often to check ground station health.<br/>
          <br/>
            <i>Default</i>: 30s<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### GroundStationLifecycle.status
<sup><sup>[↩ Parent](#groundstationlifecycle)</sup></sup>



GroundStationLifecycleStatus defines the observed state of a ground station.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#groundstationlifecyclestatusconditionsindex">conditions</a></b></td>
        <td>[]object</td>
        <td>
          conditions represent the current state of the ground station.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>firmwareUpdateStarted</b></td>
        <td>string</td>
        <td>
          firmwareUpdateStarted is when the current firmware update began.
Used for timeout detection.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>firmwareVersion</b></td>
        <td>string</td>
        <td>
          firmwareVersion is the currently running firmware version.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>k8sVersion</b></td>
        <td>string</td>
        <td>
          k8sVersion is the Kubernetes version running on the edge node.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>lastHealthCheck</b></td>
        <td>string</td>
        <td>
          lastHealthCheck is the timestamp of the last successful health check.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>phase</b></td>
        <td>enum</td>
        <td>
          phase is the current lifecycle phase.<br/>
          <br/>
            <i>Enum</i>: Provisioning, Running, Degraded, Offline, Updating<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### GroundStationLifecycle.status.conditions[index]
<sup><sup>[↩ Parent](#groundstationlifecyclestatus)</sup></sup>



Condition contains details for one aspect of the current state of this API Resource.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>lastTransitionTime</b></td>
        <td>string</td>
        <td>
          lastTransitionTime is the last time the condition transitioned from one status to another.
This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>message</b></td>
        <td>string</td>
        <td>
          message is a human readable message indicating details about the transition.
This may be an empty string.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>reason</b></td>
        <td>string</td>
        <td>
          reason contains a programmatic identifier indicating the reason for the condition's last transition.
Producers of specific condition types may define expected values and meanings for this field,
and whether the values are considered a guaranteed API.
The value should be a CamelCase string.
This field may not be empty.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>status</b></td>
        <td>enum</td>
        <td>
          status of the condition, one of True, False, Unknown.<br/>
          <br/>
            <i>Enum</i>: True, False, Unknown<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>string</td>
        <td>
          type of condition in CamelCase or in foo.example.com/CamelCase.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>observedGeneration</b></td>
        <td>integer</td>
        <td>
          observedGeneration represents the .metadata.generation that the condition was set based upon.
For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date
with respect to the current state of the instance.<br/>
          <br/>
            <i>Format</i>: int64<br/>
            <i>Minimum</i>: 0<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>

## NTNCellConfig
<sup><sup>[↩ Parent](#ntnoperatorsdevv1alpha1 )</sup></sup>






NTNCellConfig manages NTN-specific radio parameters for a gNB cell,
delegating configuration to the specified NTN backend provider.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
      <td><b>apiVersion</b></td>
      <td>string</td>
      <td>ntn.operators.dev/v1alpha1</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b>kind</b></td>
      <td>string</td>
      <td>NTNCellConfig</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta">metadata</a></b></td>
      <td>object</td>
      <td>Refer to the Kubernetes API documentation for the fields of the `metadata` field.</td>
      <td>true</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspec">spec</a></b></td>
        <td>object</td>
        <td>
          NTNCellConfigSpec defines the desired NTN cell configuration.<br/>
          <br/>
            <i>Validations</i>:<li>!has(self.provider.remoteControl) || has(self.cellID): cellID is required when provider.remoteControl is set (runtime push targets a cell by plmn+nci)</li>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigstatus">status</a></b></td>
        <td>object</td>
        <td>
          NTNCellConfigStatus defines the observed state of NTNCellConfig.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec
<sup><sup>[↩ Parent](#ntncellconfig)</sup></sup>



NTNCellConfigSpec defines the desired NTN cell configuration.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#ntncellconfigspecntn">ntn</a></b></td>
        <td>object</td>
        <td>
          ntn contains NTN-specific radio parameters per 3GPP TS 38.213 / OCUDU geo_ntn.yml.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.ephemerisECEF) || has(self.ephemerisOrbital): exactly one of ephemerisECEF or ephemerisOrbital must be set</li><li>!(has(self.ephemerisECEF) && has(self.ephemerisOrbital)): ephemerisECEF and ephemerisOrbital are mutually exclusive</li><li>!has(self.ephemerisECEF) || self.ephemerisECEF.posX != 0 || self.ephemerisECEF.posY != 0 || self.ephemerisECEF.posZ != 0: ephemerisECEF position must not be all zeros</li>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecprovider">provider</a></b></td>
        <td>object</td>
        <td>
          provider specifies which NTN backend to configure.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspeccellid">cellID</a></b></td>
        <td>object</td>
        <td>
          cellID identifies the OCUDU cell (plmn + nci) that runtime remote commands
target. Required when provider.remoteControl is set; the value must match
the cell the gNB booted with. Unset ⇒ ConfigMap bootstrap path only.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspeccelloverrides">cellOverrides</a></b></td>
        <td>object</td>
        <td>
          cellOverrides allows fine-tuning PUCCH, PDSCH, PRACH, and RRC parameters.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ephemerisNoradID</b></td>
        <td>integer</td>
        <td>
          ephemerisNoradID selects which satellite's propagated state vector, from the
referenced SatelliteEphemeris (ephemerisRef), to push at runtime (#176). When
unset, the referenced ephemeris's first propagated state is used.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ephemerisRef</b></td>
        <td>string</td>
        <td>
          ephemerisRef is the name of a SatelliteEphemeris CR in the same namespace.
When set, the controller re-reconciles this NTNCellConfig whenever the
referenced SatelliteEphemeris is updated and invokes runtime ephemeris push
on the provider reconcile path. The static ephemeris in spec.ntn
(ephemerisECEF or ephemerisOrbital) remains required as the source payload.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn
<sup><sup>[↩ Parent](#ntncellconfigspec)</sup></sup>



ntn contains NTN-specific radio parameters per 3GPP TS 38.213 / OCUDU geo_ntn.yml.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>cellSpecificKoffset</b></td>
        <td>integer</td>
        <td>
          cellSpecificKoffset is the cell-specific K_offset for NTN scheduling timing
(3GPP TS 38.213 / TS 38.300 §16.14.2). Its unit is milliseconds: OCUDU stores
cell_specific_koffset as std::chrono::milliseconds and converts it to
operating-SCS slots internally, so the value passes through this operator
unchanged with no unit conversion. (3GPP expresses K_offset as a slot count
assuming the 15 kHz reference SCS, where 1 slot = 1 ms; that identity is only
how the IE is defined, not a conversion the user applies here.)
The 3GPP IE cellSpecificKoffset-r17 is INTEGER(0..1023), but OCUDU rejects 0
(its CLI and config validation enforce 1-1023), so Minimum is 1 to mirror the
backend rather than the spec.<br/>
          <br/>
            <i>Default</i>: 150<br/>
            <i>Minimum</i>: 1<br/>
            <i>Maximum</i>: 1023<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>distanceThreshold</b></td>
        <td>integer</td>
        <td>
          distanceThreshold sets the distance threshold for cell
selection in metres.<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnephemerisecef">ephemerisECEF</a></b></td>
        <td>object</td>
        <td>
          ephemerisECEF defines the satellite position and velocity in ECEF coordinates.
Mutually exclusive with ephemerisOrbital.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnephemerisorbital">ephemerisOrbital</a></b></td>
        <td>object</td>
        <td>
          ephemerisOrbital defines the satellite orbit using Keplerian elements.
Mutually exclusive with ephemerisECEF. Preferred for LEO satellites
where source data is in OMM/TLE form (CelesTrak, SpaceTrack).<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnepochtime">epochTime</a></b></td>
        <td>object</td>
        <td>
          epochTime defines the SFN/subframe reference for NTN timing alignment.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnfeederlinkinfo">feederLinkInfo</a></b></td>
        <td>object</td>
        <td>
          feederLinkInfo provides feeder link parameters for Doppler compensation.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnmovingreflocation">movingRefLocation</a></b></td>
        <td>object</td>
        <td>
          movingRefLocation defines the Earth-moving reference location for LEO NTN cells.
3GPP Release 18 SIB19 field. Used by UEs for timing/Doppler estimation.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnneighborcellsindex">neighborCells</a></b></td>
        <td>[]object</td>
        <td>
          neighborCells lists neighbor NTN cells for measurement/handover.
OCUDU YAML renders as "ncells:" for compatibility.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnntngatewaylocation">ntnGatewayLocation</a></b></td>
        <td>object</td>
        <td>
          ntnGatewayLocation specifies the NTN gateway (ground station) coordinates.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ntnUlSyncValidityDur</b></td>
        <td>integer</td>
        <td>
          ntnUlSyncValidityDur sets the UL synchronization validity duration in seconds.<br/>
          <br/>
            <i>Enum</i>: 5, 10, 15, 20, 25, 30, 35, 40, 45, 50, 55, 60, 120, 180, 240, 900<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>payloadType</b></td>
        <td>enum</td>
        <td>
          payloadType specifies the satellite payload architecture.<br/>
          <br/>
            <i>Enum</i>: transparent, regenerative<br/>
            <i>Default</i>: transparent<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnpolarization">polarization</a></b></td>
        <td>object</td>
        <td>
          polarization specifies the antenna polarization for downlink and uplink.
Per 3GPP TS 38.331 SIB19, ntn-PolarizationDL-r17 and ntn-PolarizationUL-r17
are independent IEs. OCUDU collapses them under a single `polarization:` map
with `dl:` / `ul:` sub-keys, matching this CRD layout.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.dl) || has(self.ul): at least one of dl or ul must be set</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnreferencelocation">referenceLocation</a></b></td>
        <td>object</td>
        <td>
          referenceLocation defines the NTN cell reference location.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnsatswitchwithresync">satSwitchWithResync</a></b></td>
        <td>object</td>
        <td>
          satSwitchWithResync provides satellite switch handover hints to UEs during
satellite-to-satellite transitions. 3GPP Release 18 SIB19 field.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>tService</b></td>
        <td>integer</td>
        <td>
          tService sets the expected NTN service duration in seconds.<br/>
          <br/>
            <i>Minimum</i>: 1<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>taCommon</b></td>
        <td>integer</td>
        <td>
          taCommon sets the common Timing Advance value (0-66485757).<br/>
          <br/>
            <i>Default</i>: 0<br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 6.6485756e+07<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntntainfo">taInfo</a></b></td>
        <td>object</td>
        <td>
          taInfo provides extended Timing Advance parameters per 3GPP TS 38.213.
When set, taInfo.taCommon takes precedence over the top-level taCommon field.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>taReport</b></td>
        <td>boolean</td>
        <td>
          taReport enables UE TA reporting.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.ephemerisECEF
<sup><sup>[↩ Parent](#ntncellconfigspecntn)</sup></sup>



ephemerisECEF defines the satellite position and velocity in ECEF coordinates.
Mutually exclusive with ephemerisOrbital.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>posX</b></td>
        <td>integer</td>
        <td>
          posX is the X position in 1.3 m/LSB codepoints (3GPP positionX-r17,
-33554432 to 33554431). The operator emits posX × 1.3 as metres to OCUDU.<br/>
          <br/>
            <i>Minimum</i>: -3.3554432e+07<br/>
            <i>Maximum</i>: 3.355443e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>posY</b></td>
        <td>integer</td>
        <td>
          posY is the Y position in 1.3 m/LSB codepoints (-33554432 to 33554431).<br/>
          <br/>
            <i>Minimum</i>: -3.3554432e+07<br/>
            <i>Maximum</i>: 3.355443e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>posZ</b></td>
        <td>integer</td>
        <td>
          posZ is the Z position in 1.3 m/LSB codepoints (-33554432 to 33554431).<br/>
          <br/>
            <i>Minimum</i>: -3.3554432e+07<br/>
            <i>Maximum</i>: 3.355443e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>velX</b></td>
        <td>integer</td>
        <td>
          velX is the X velocity in 0.06 m/s/LSB codepoints (3GPP velocityVX-r17,
-131072 to 131071; 0 for GEO). The operator emits velX × 0.06 as m/s.<br/>
          <br/>
            <i>Default</i>: 0<br/>
            <i>Minimum</i>: -131072<br/>
            <i>Maximum</i>: 131071<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>velY</b></td>
        <td>integer</td>
        <td>
          velY is the Y velocity in 0.06 m/s/LSB codepoints (-131072 to 131071; 0 for GEO).<br/>
          <br/>
            <i>Default</i>: 0<br/>
            <i>Minimum</i>: -131072<br/>
            <i>Maximum</i>: 131071<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>velZ</b></td>
        <td>integer</td>
        <td>
          velZ is the Z velocity in 0.06 m/s/LSB codepoints (-131072 to 131071; 0 for GEO).<br/>
          <br/>
            <i>Default</i>: 0<br/>
            <i>Minimum</i>: -131072<br/>
            <i>Maximum</i>: 131071<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.ephemerisOrbital
<sup><sup>[↩ Parent](#ntncellconfigspecntn)</sup></sup>



ephemerisOrbital defines the satellite orbit using Keplerian elements.
Mutually exclusive with ephemerisECEF. Preferred for LEO satellites
where source data is in OMM/TLE form (CelesTrak, SpaceTrack).

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>argOfPeriapsis</b></td>
        <td>integer</td>
        <td>
          argOfPeriapsis is the argument of periapsis in 1e-4 degrees (0-3600000).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 3.6e+06<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>eccentricity</b></td>
        <td>integer</td>
        <td>
          eccentricity is the orbital eccentricity scaled by 1e6 (0-15005). The
operator emits eccentricity × 1e-6; OCUDU accepts e ≤ 0.01500510825.<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 15005<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>inclination</b></td>
        <td>integer</td>
        <td>
          inclination is the orbital inclination in 1e-4 degrees; the operator emits
inclination × π/1.8e6 as radians. OCUDU's orbital ephemeris accepts only
[0°, 90°] (0 to +π/2 rad), so inclinations above 90° (e.g. sun-synchronous
~98°, retrograde) are NOT representable via the orbital path — use
ephemerisECEF (SGP4 state vector) for those. Max is 900000 (90°).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 900000<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>meanAnomaly</b></td>
        <td>integer</td>
        <td>
          meanAnomaly is the mean anomaly in 1e-4 degrees (0-3600000).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 3.6e+06<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>rightAscension</b></td>
        <td>integer</td>
        <td>
          rightAscension is the right ascension of the ascending node in 1e-4 degrees (0-3600000).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 3.6e+06<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>semiMajorAxis</b></td>
        <td>integer</td>
        <td>
          semiMajorAxis is the semi-major axis in metres, emitted as-is to OCUDU
(which accepts 6500000-42998632 m).<br/>
          <br/>
            <i>Minimum</i>: 6.5e+06<br/>
            <i>Maximum</i>: 4.2998632e+07<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.epochTime
<sup><sup>[↩ Parent](#ntncellconfigspecntn)</sup></sup>



epochTime defines the SFN/subframe reference for NTN timing alignment.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>sfn</b></td>
        <td>integer</td>
        <td>
          sfn is the System Frame Number (0-1023).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 1023<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>subframeNumber</b></td>
        <td>integer</td>
        <td>
          subframeNumber is the subframe within the SFN (0-9).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 9<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.feederLinkInfo
<sup><sup>[↩ Parent](#ntncellconfigspecntn)</sup></sup>



feederLinkInfo provides feeder link parameters for Doppler compensation.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>dlFreqHz</b></td>
        <td>integer</td>
        <td>
          dlFreqHz is the downlink frequency in Hz. Required when feederLinkInfo is set.<br/>
          <br/>
            <i>Format</i>: int64<br/>
            <i>Minimum</i>: 1<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>enableDopplerCompensation</b></td>
        <td>boolean</td>
        <td>
          enableDopplerCompensation enables feeder link Doppler compensation.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>ulFreqHz</b></td>
        <td>integer</td>
        <td>
          ulFreqHz is the uplink frequency in Hz. Required when feederLinkInfo is set.<br/>
          <br/>
            <i>Format</i>: int64<br/>
            <i>Minimum</i>: 1<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.movingRefLocation
<sup><sup>[↩ Parent](#ntncellconfigspecntn)</sup></sup>



movingRefLocation defines the Earth-moving reference location for LEO NTN cells.
3GPP Release 18 SIB19 field. Used by UEs for timing/Doppler estimation.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>latitude</b></td>
        <td>integer</td>
        <td>
          latitude in 1e-4 degrees (-900000 to 900000 = -90° to 90°).<br/>
          <br/>
            <i>Minimum</i>: -900000<br/>
            <i>Maximum</i>: 900000<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>longitude</b></td>
        <td>integer</td>
        <td>
          longitude in 1e-4 degrees (-1800000 to 1800000 = -180° to 180°).<br/>
          <br/>
            <i>Minimum</i>: -1.8e+06<br/>
            <i>Maximum</i>: 1.8e+06<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.neighborCells[index]
<sup><sup>[↩ Parent](#ntncellconfigspecntn)</sup></sup>



NTNNeighborCell describes a neighbor NTN cell.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>physicalCellID</b></td>
        <td>integer</td>
        <td>
          physicalCellID of the neighbor (0-1007).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 1007<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>frequency</b></td>
        <td>integer</td>
        <td>
          frequency is the neighbor cell's ARFCN (NR-ARFCN, always >= 1).<br/>
          <br/>
            <i>Minimum</i>: 1<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.ntnGatewayLocation
<sup><sup>[↩ Parent](#ntncellconfigspecntn)</sup></sup>



ntnGatewayLocation specifies the NTN gateway (ground station) coordinates.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>altitude</b></td>
        <td>integer</td>
        <td>
          altitude in metres above sea level. Required when ntnGatewayLocation is set.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>latitude</b></td>
        <td>integer</td>
        <td>
          latitude in 1e-4 degrees (-900000 to 900000).<br/>
          <br/>
            <i>Minimum</i>: -900000<br/>
            <i>Maximum</i>: 900000<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>longitude</b></td>
        <td>integer</td>
        <td>
          longitude in 1e-4 degrees (-1800000 to 1800000).<br/>
          <br/>
            <i>Minimum</i>: -1.8e+06<br/>
            <i>Maximum</i>: 1.8e+06<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.polarization
<sup><sup>[↩ Parent](#ntncellconfigspecntn)</sup></sup>



polarization specifies the antenna polarization for downlink and uplink.
Per 3GPP TS 38.331 SIB19, ntn-PolarizationDL-r17 and ntn-PolarizationUL-r17
are independent IEs. OCUDU collapses them under a single `polarization:` map
with `dl:` / `ul:` sub-keys, matching this CRD layout.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>dl</b></td>
        <td>enum</td>
        <td>
          dl is the downlink polarization broadcast in SIB19 ntn-PolarizationDL-r17.<br/>
          <br/>
            <i>Enum</i>: rhcp, lhcp, linear<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ul</b></td>
        <td>enum</td>
        <td>
          ul is the uplink polarization broadcast in SIB19 ntn-PolarizationUL-r17.<br/>
          <br/>
            <i>Enum</i>: rhcp, lhcp, linear<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.referenceLocation
<sup><sup>[↩ Parent](#ntncellconfigspecntn)</sup></sup>



referenceLocation defines the NTN cell reference location.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>latitude</b></td>
        <td>integer</td>
        <td>
          latitude in 1e-4 degrees (-900000 to 900000).<br/>
          <br/>
            <i>Minimum</i>: -900000<br/>
            <i>Maximum</i>: 900000<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>longitude</b></td>
        <td>integer</td>
        <td>
          longitude in 1e-4 degrees (-1800000 to 1800000).<br/>
          <br/>
            <i>Minimum</i>: -1.8e+06<br/>
            <i>Maximum</i>: 1.8e+06<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.satSwitchWithResync
<sup><sup>[↩ Parent](#ntncellconfigspecntn)</sup></sup>



satSwitchWithResync provides satellite switch handover hints to UEs during
satellite-to-satellite transitions. 3GPP Release 18 SIB19 field.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#ntncellconfigspecntnsatswitchwithresyncntnconfig">ntnConfig</a></b></td>
        <td>object</td>
        <td>
          ntnConfig is the target satellite's NTN configuration after the switch.
Required: OCUDU rejects a sat_switch_with_resync that has no ntn_cfg.<br/>
          <br/>
            <i>Validations</i>:<li>has(self.ephemerisECEF) || has(self.ephemerisOrbital): exactly one of ephemerisECEF or ephemerisOrbital must be set</li><li>!(has(self.ephemerisECEF) && has(self.ephemerisOrbital)): ephemerisECEF and ephemerisOrbital are mutually exclusive</li><li>!has(self.ephemerisECEF) || self.ephemerisECEF.posX != 0 || self.ephemerisECEF.posY != 0 || self.ephemerisECEF.posZ != 0: ephemerisECEF position must not be all zeros</li>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>epochUnixMs</b></td>
        <td>integer</td>
        <td>
          epochUnixMs is the reference epoch for the target assistance info, in Unix
milliseconds. 0 omits the field. Unlike the serving-cell epoch, OCUDU does
not require the sat-switch epoch to be in the future.<br/>
          <br/>
            <i>Format</i>: int64<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnsatswitchwithresyncgatewaylocation">gatewayLocation</a></b></td>
        <td>object</td>
        <td>
          gatewayLocation is the target satellite's NTN gateway (feeder) location,
emitted as OCUDU's ntn_gateway_location geodetic coordinates.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ssbTimeOffsetSubframes</b></td>
        <td>integer</td>
        <td>
          ssbTimeOffsetSubframes is the SSB time offset in subframes (0-159), mapping
to OCUDU's ssb_time_offset_sf.<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 159<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>tServiceStartUnixMs</b></td>
        <td>integer</td>
        <td>
          tServiceStartUnixMs is when the target satellite starts serving, in Unix
milliseconds. 0 omits the field.<br/>
          <br/>
            <i>Format</i>: int64<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.satSwitchWithResync.ntnConfig
<sup><sup>[↩ Parent](#ntncellconfigspecntnsatswitchwithresync)</sup></sup>



ntnConfig is the target satellite's NTN configuration after the switch.
Required: OCUDU rejects a sat_switch_with_resync that has no ntn_cfg.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>cellSpecificKoffset</b></td>
        <td>integer</td>
        <td>
          cellSpecificKoffset is the target cell-specific K_offset in milliseconds,
same semantics as NTNParams.cellSpecificKoffset: 1-1023, with Minimum 1
because OCUDU rejects 0. Omit the field to leave it unset.<br/>
          <br/>
            <i>Minimum</i>: 1<br/>
            <i>Maximum</i>: 1023<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnsatswitchwithresyncntnconfigephemerisecef">ephemerisECEF</a></b></td>
        <td>object</td>
        <td>
          ephemerisECEF is the target satellite's ECEF state vector.
Mutually exclusive with ephemerisOrbital.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnsatswitchwithresyncntnconfigephemerisorbital">ephemerisOrbital</a></b></td>
        <td>object</td>
        <td>
          ephemerisOrbital is the target satellite's Keplerian elements.
Mutually exclusive with ephemerisECEF.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>kMac</b></td>
        <td>integer</td>
        <td>
          kMac is the MAC-CE scheduling offset k_mac (3GPP kmac-r17, INTEGER 1..512).
It tunes the k-offset applied to MAC CE contention-based resolution so UE
MAC feedback stays time-aligned with the satellite round-trip; distinct from
cellSpecificKoffset (PUSCH/PDSCH offset).

This is the ONLY OCUDU surface that accepts k_mac (issue #52): the runtime
ntn_config_update command's sat_switch_with_resync.ntn_cfg. k_mac is NOT in
OCUDU's bootstrap YAML on any cell (serving, neighbor, or satswitch), so the
serving-cell static config remains 7/8 on the ntn_config field set by design.

The 1..512 bound here is LOAD-BEARING: OCUDU's runtime update path does not
range-check k_mac (verified against a live gNB — it accepts out-of-range
values that would then violate the ASN.1 kmac-r17 constraint), so this CRD
validation is the only guard keeping a 3GPP-invalid k_mac off the wire.<br/>
          <br/>
            <i>Minimum</i>: 1<br/>
            <i>Maximum</i>: 512<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>ntnUlSyncValidityDur</b></td>
        <td>integer</td>
        <td>
          ntnUlSyncValidityDur is the target UL-sync validity duration in seconds.
OCUDU keys this ntn_ul_sync_validity_dur inside ntn_cfg — deliberately
distinct from the serving-cell ntn_ul_sync_validity_duration key.<br/>
          <br/>
            <i>Enum</i>: 5, 10, 15, 20, 25, 30, 35, 40, 45, 50, 55, 60, 120, 180, 240, 900<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecntnsatswitchwithresyncntnconfigtainfo">taInfo</a></b></td>
        <td>object</td>
        <td>
          taInfo provides the target satellite's timing-advance parameters. The
runtime ntn_cfg accepts only ta_common / ta_common_drift /
ta_common_drift_variant — NOT ta_common_offset (that key is YAML-only), so
taInfo.taCommonOffset is ignored when pushed here.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>taReport</b></td>
        <td>boolean</td>
        <td>
          taReport enables UE TA reporting for the target satellite.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.satSwitchWithResync.ntnConfig.ephemerisECEF
<sup><sup>[↩ Parent](#ntncellconfigspecntnsatswitchwithresyncntnconfig)</sup></sup>



ephemerisECEF is the target satellite's ECEF state vector.
Mutually exclusive with ephemerisOrbital.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>posX</b></td>
        <td>integer</td>
        <td>
          posX is the X position in 1.3 m/LSB codepoints (3GPP positionX-r17,
-33554432 to 33554431). The operator emits posX × 1.3 as metres to OCUDU.<br/>
          <br/>
            <i>Minimum</i>: -3.3554432e+07<br/>
            <i>Maximum</i>: 3.355443e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>posY</b></td>
        <td>integer</td>
        <td>
          posY is the Y position in 1.3 m/LSB codepoints (-33554432 to 33554431).<br/>
          <br/>
            <i>Minimum</i>: -3.3554432e+07<br/>
            <i>Maximum</i>: 3.355443e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>posZ</b></td>
        <td>integer</td>
        <td>
          posZ is the Z position in 1.3 m/LSB codepoints (-33554432 to 33554431).<br/>
          <br/>
            <i>Minimum</i>: -3.3554432e+07<br/>
            <i>Maximum</i>: 3.355443e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>velX</b></td>
        <td>integer</td>
        <td>
          velX is the X velocity in 0.06 m/s/LSB codepoints (3GPP velocityVX-r17,
-131072 to 131071; 0 for GEO). The operator emits velX × 0.06 as m/s.<br/>
          <br/>
            <i>Default</i>: 0<br/>
            <i>Minimum</i>: -131072<br/>
            <i>Maximum</i>: 131071<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>velY</b></td>
        <td>integer</td>
        <td>
          velY is the Y velocity in 0.06 m/s/LSB codepoints (-131072 to 131071; 0 for GEO).<br/>
          <br/>
            <i>Default</i>: 0<br/>
            <i>Minimum</i>: -131072<br/>
            <i>Maximum</i>: 131071<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>velZ</b></td>
        <td>integer</td>
        <td>
          velZ is the Z velocity in 0.06 m/s/LSB codepoints (-131072 to 131071; 0 for GEO).<br/>
          <br/>
            <i>Default</i>: 0<br/>
            <i>Minimum</i>: -131072<br/>
            <i>Maximum</i>: 131071<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.satSwitchWithResync.ntnConfig.ephemerisOrbital
<sup><sup>[↩ Parent](#ntncellconfigspecntnsatswitchwithresyncntnconfig)</sup></sup>



ephemerisOrbital is the target satellite's Keplerian elements.
Mutually exclusive with ephemerisECEF.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>argOfPeriapsis</b></td>
        <td>integer</td>
        <td>
          argOfPeriapsis is the argument of periapsis in 1e-4 degrees (0-3600000).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 3.6e+06<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>eccentricity</b></td>
        <td>integer</td>
        <td>
          eccentricity is the orbital eccentricity scaled by 1e6 (0-15005). The
operator emits eccentricity × 1e-6; OCUDU accepts e ≤ 0.01500510825.<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 15005<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>inclination</b></td>
        <td>integer</td>
        <td>
          inclination is the orbital inclination in 1e-4 degrees; the operator emits
inclination × π/1.8e6 as radians. OCUDU's orbital ephemeris accepts only
[0°, 90°] (0 to +π/2 rad), so inclinations above 90° (e.g. sun-synchronous
~98°, retrograde) are NOT representable via the orbital path — use
ephemerisECEF (SGP4 state vector) for those. Max is 900000 (90°).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 900000<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>meanAnomaly</b></td>
        <td>integer</td>
        <td>
          meanAnomaly is the mean anomaly in 1e-4 degrees (0-3600000).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 3.6e+06<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>rightAscension</b></td>
        <td>integer</td>
        <td>
          rightAscension is the right ascension of the ascending node in 1e-4 degrees (0-3600000).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 3.6e+06<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>semiMajorAxis</b></td>
        <td>integer</td>
        <td>
          semiMajorAxis is the semi-major axis in metres, emitted as-is to OCUDU
(which accepts 6500000-42998632 m).<br/>
          <br/>
            <i>Minimum</i>: 6.5e+06<br/>
            <i>Maximum</i>: 4.2998632e+07<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.satSwitchWithResync.ntnConfig.taInfo
<sup><sup>[↩ Parent](#ntncellconfigspecntnsatswitchwithresyncntnconfig)</sup></sup>



taInfo provides the target satellite's timing-advance parameters. The
runtime ntn_cfg accepts only ta_common / ta_common_drift /
ta_common_drift_variant — NOT ta_common_offset (that key is YAML-only), so
taInfo.taCommonOffset is ignored when pushed here.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>taCommon</b></td>
        <td>integer</td>
        <td>
          taCommon is the common Timing Advance value (0-66485757). Required when
taInfo is set — explicitly provide 0 for GEO satellites.<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 6.6485756e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>taCommonDrift</b></td>
        <td>integer</td>
        <td>
          taCommonDrift is the TA drift rate in 3GPP codepoints (ta-CommonDrift-r17,
-257303 to 257303). The operator emits taCommonDrift × 2e-4 as µs/s
(OCUDU accepts ±51.4606 µs/s).<br/>
          <br/>
            <i>Minimum</i>: -257303<br/>
            <i>Maximum</i>: 257303<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>taCommonDriftVariant</b></td>
        <td>integer</td>
        <td>
          taCommonDriftVariant is the TA drift-rate variant in codepoints
(ta-CommonDriftVariant-r17, 0 to 28949). Emitted × 2e-5 as µs/s²
(OCUDU accepts 0-0.57898 µs/s²).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 28949<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>taCommonOffset</b></td>
        <td>integer</td>
        <td>
          taCommonOffset is an additional common-TA offset in codepoints (same
0.004072 µs granularity as taCommon; 0 to 2455796 maps to OCUDU's 0-10000 µs).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 2.455795e+06<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.satSwitchWithResync.gatewayLocation
<sup><sup>[↩ Parent](#ntncellconfigspecntnsatswitchwithresync)</sup></sup>



gatewayLocation is the target satellite's NTN gateway (feeder) location,
emitted as OCUDU's ntn_gateway_location geodetic coordinates.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>altitude</b></td>
        <td>integer</td>
        <td>
          altitude in metres above sea level. Required when ntnGatewayLocation is set.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>latitude</b></td>
        <td>integer</td>
        <td>
          latitude in 1e-4 degrees (-900000 to 900000).<br/>
          <br/>
            <i>Minimum</i>: -900000<br/>
            <i>Maximum</i>: 900000<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>longitude</b></td>
        <td>integer</td>
        <td>
          longitude in 1e-4 degrees (-1800000 to 1800000).<br/>
          <br/>
            <i>Minimum</i>: -1.8e+06<br/>
            <i>Maximum</i>: 1.8e+06<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.ntn.taInfo
<sup><sup>[↩ Parent](#ntncellconfigspecntn)</sup></sup>



taInfo provides extended Timing Advance parameters per 3GPP TS 38.213.
When set, taInfo.taCommon takes precedence over the top-level taCommon field.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>taCommon</b></td>
        <td>integer</td>
        <td>
          taCommon is the common Timing Advance value (0-66485757). Required when
taInfo is set — explicitly provide 0 for GEO satellites.<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 6.6485756e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>taCommonDrift</b></td>
        <td>integer</td>
        <td>
          taCommonDrift is the TA drift rate in 3GPP codepoints (ta-CommonDrift-r17,
-257303 to 257303). The operator emits taCommonDrift × 2e-4 as µs/s
(OCUDU accepts ±51.4606 µs/s).<br/>
          <br/>
            <i>Minimum</i>: -257303<br/>
            <i>Maximum</i>: 257303<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>taCommonDriftVariant</b></td>
        <td>integer</td>
        <td>
          taCommonDriftVariant is the TA drift-rate variant in codepoints
(ta-CommonDriftVariant-r17, 0 to 28949). Emitted × 2e-5 as µs/s²
(OCUDU accepts 0-0.57898 µs/s²).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 28949<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>taCommonOffset</b></td>
        <td>integer</td>
        <td>
          taCommonOffset is an additional common-TA offset in codepoints (same
0.004072 µs granularity as taCommon; 0 to 2455796 maps to OCUDU's 0-10000 µs).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 2.455795e+06<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.provider
<sup><sup>[↩ Parent](#ntncellconfigspec)</sup></sup>



provider specifies which NTN backend to configure.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>type</b></td>
        <td>enum</td>
        <td>
          type is the provider type. Currently only "ocudu" is supported.<br/>
          <br/>
            <i>Enum</i>: ocudu<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>endpoint</b></td>
        <td>string</td>
        <td>
          endpoint is the provider-specific endpoint (e.g., O1 NETCONF address).<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>namespace</b></td>
        <td>string</td>
        <td>
          namespace where the provider resources (e.g., OCUDU gNB) are deployed.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecproviderremotecontrol">remoteControl</a></b></td>
        <td>object</td>
        <td>
          remoteControl configures the gNB remote_control WebSocket for live NTN
config push. When set together with spec.cellID, the operator pushes runtime
ntn_config_update commands; otherwise it uses the ConfigMap path only.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.provider.remoteControl
<sup><sup>[↩ Parent](#ntncellconfigspecprovider)</sup></sup>



remoteControl configures the gNB remote_control WebSocket for live NTN
config push. When set together with spec.cellID, the operator pushes runtime
ntn_config_update commands; otherwise it uses the ConfigMap path only.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>endpoint</b></td>
        <td>string</td>
        <td>
          endpoint is host:port of the gNB remote_control server — a hostname/IPv4
("127.0.0.1:8001") or a bracketed IPv6 literal ("[::1]:8001") for dual-stack
clusters. The provider prepends ws://, so include NEITHER a scheme nor a path
— a value like "ws://host:8001" would dial "ws://ws://host:8001" and fail.
Validation is layered: the pattern enforces the bare host:port shape with a
DNS-1123 hostname or bracketed IPv6, and CEL rules enforce the port range
(1-65535), that a bracketed host is a valid IP, that an all-numeric host is a
valid IPv4 (so "999.999.999.999:1" is rejected, not treated as a hostname), and
that a DNS host obeys the RFC 1035 length limits (whole name <= 253, each label
1-63) — a permanent admission error beats a silent tight-requeue on a mistyped
value. The pattern alone cannot bound the label/host length (a regex quantifier
would, but the DNS-1123 label form makes that unreadable), so CEL carries it.<br/>
          <br/>
            <i>Validations</i>:<li>int(self.substring(self.lastIndexOf(':') + 1)) >= 1 && int(self.substring(self.lastIndexOf(':') + 1)) <= 65535: endpoint port must be between 1 and 65535</li><li>!self.startsWith('[') || isIP(self.substring(1, self.lastIndexOf(']'))): a bracketed endpoint host must be a valid IP address</li><li>!self.substring(0, self.lastIndexOf(':')).matches('^[0-9.]+$') || isIP(self.substring(0, self.lastIndexOf(':'))): an all-numeric endpoint host must be a valid IPv4 address</li><li>self.startsWith('[') || (self.substring(0, self.lastIndexOf(':')).size() <= 253 && self.substring(0, self.lastIndexOf(':')).split('.').all(l, l.size() >= 1 && l.size() <= 63)): endpoint host must be a DNS name of at most 253 characters with each dot-separated label 1-63 characters</li>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspecproviderremotecontroltls">tls</a></b></td>
        <td>object</td>
        <td>
          tls, when set, secures the runtime push: the provider dials wss:// (TLS)
instead of plaintext ws:// and authenticates with the material in the
referenced Secret. Omit it to keep the plaintext ws:// behavior (N-12).<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.provider.remoteControl.tls
<sup><sup>[↩ Parent](#ntncellconfigspecproviderremotecontrol)</sup></sup>



tls, when set, secures the runtime push: the provider dials wss:// (TLS)
instead of plaintext ws:// and authenticates with the material in the
referenced Secret. Omit it to keep the plaintext ws:// behavior (N-12).

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>mode</b></td>
        <td>enum</td>
        <td>
          mode selects the transport-security posture:
  "tls"  — dial wss://, verify the server certificate, and (if the Secret
           carries a token) send it as an Authorization: Bearer header.
  "mtls" — additionally present a client certificate (mutual TLS); the
           Secret MUST then carry tls.crt + tls.key.<br/>
          <br/>
            <i>Enum</i>: tls, mtls<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>secretName</b></td>
        <td>string</td>
        <td>
          secretName is the Secret (in this NTNCellConfig's namespace) holding the TLS
trust and auth material. Recognized keys: "ca.crt" (PEM CA to verify the
gNB/proxy server certificate — omit to use the system roots), "token" (the
shared secret sent as Authorization: Bearer — optional), and, for mode=mtls,
"tls.crt" + "tls.key" (the client certificate/key). A bare shared secret is
replayable, so it is only ever sent over the wss:// (TLS) connection.

SECURITY: the Secret's owner must opt it in for remote-control use with the label
"ntn.operators.dev/remote-control-credential: true", and a Kubernetes API
credential (a service-account or bootstrap-token Secret) is refused. This is a
mitigation, not a full authorization boundary: the opt-in is namespace-scoped, so
ANY NTNCellConfig in the namespace may use a labelled Secret. Do not grant
NTNCellConfig write to principals who should not be able to use every labelled
remote-control credential in that namespace.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>serverName</b></td>
        <td>string</td>
        <td>
          serverName overrides the TLS ServerName (SNI) verified against the server
certificate's SubjectAltNames. Defaults to the endpoint host. Set it when the
gNB/proxy certificate's SAN does not match the dialed host (e.g. an IP
endpoint fronted by a DNS-named certificate).<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.cellID
<sup><sup>[↩ Parent](#ntncellconfigspec)</sup></sup>



cellID identifies the OCUDU cell (plmn + nci) that runtime remote commands
target. Required when provider.remoteControl is set; the value must match
the cell the gNB booted with. Unset ⇒ ConfigMap bootstrap path only.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>nci</b></td>
        <td>integer</td>
        <td>
          nci is the 36-bit NR Cell Identity (0 to 2^36-1).<br/>
          <br/>
            <i>Format</i>: int64<br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 6.8719476735e+10<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>plmn</b></td>
        <td>string</td>
        <td>
          plmn is the cell's PLMN, 5 or 6 digits (e.g. "00101").<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.cellOverrides
<sup><sup>[↩ Parent](#ntncellconfigspec)</sup></sup>



cellOverrides allows fine-tuning PUCCH, PDSCH, PRACH, and RRC parameters.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>pdschMaxHarqRetxs</b></td>
        <td>integer</td>
        <td>
          pdschMaxHarqRetxs sets the max HARQ retransmissions (0 = disabled for NTN).<br/>
          <br/>
            <i>Default</i>: 0<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>prachMaxMsg3HarqRetx</b></td>
        <td>integer</td>
        <td>
          prachMaxMsg3HarqRetx sets the max msg3 HARQ retransmissions.<br/>
          <br/>
            <i>Default</i>: 0<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>rrcGuardTimeMs</b></td>
        <td>integer</td>
        <td>
          rrcGuardTimeMs sets the RRC procedure guard time in ms.<br/>
          <br/>
            <i>Default</i>: 12800<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigspeccelloverridessibschedule">sibSchedule</a></b></td>
        <td>object</td>
        <td>
          sibSchedule tunes SIB19 broadcast scheduling. Any unset sub-field
falls back to the defaults (siWindowLength=5, siPeriod=16,
siWindowPosition=2). Tune when PDCCH capacity is tight or when
SIB19 broadcast cadence needs to track short ntn-UlSyncValidityDur.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.cellOverrides.sibSchedule
<sup><sup>[↩ Parent](#ntncellconfigspeccelloverrides)</sup></sup>



sibSchedule tunes SIB19 broadcast scheduling. Any unset sub-field
falls back to the defaults (siWindowLength=5, siPeriod=16,
siWindowPosition=2). Tune when PDCCH capacity is tight or when
SIB19 broadcast cadence needs to track short ntn-UlSyncValidityDur.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>siPeriod</b></td>
        <td>integer</td>
        <td>
          siPeriod is the SIB19 broadcast period in radio frames.
Shorter periods keep UEs' NTN assistance fresh but cost air time.<br/>
          <br/>
            <i>Enum</i>: 8, 16, 32, 64, 128, 256, 512<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>siWindowLength</b></td>
        <td>integer</td>
        <td>
          siWindowLength is the SI window length in slots. OCUDU accepts
the standard set; picking a larger value increases PDCCH pressure.<br/>
          <br/>
            <i>Enum</i>: 5, 10, 20, 40, 80, 160, 320, 640, 1280<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>siWindowPosition</b></td>
        <td>integer</td>
        <td>
          siWindowPosition is SIB19's slot offset within the SI period
(schedulingInfoList2-r17). It must be strictly greater than the number of
preceding schedulingInfoList entries; the emitter always schedules one SIB2
(ID < 15) before SIB19, so the minimum is 2. Pointer so an explicit value is
distinguished from unset (which defaults to 2).<br/>
          <br/>
            <i>Minimum</i>: 2<br/>
            <i>Maximum</i>: 79<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.status
<sup><sup>[↩ Parent](#ntncellconfig)</sup></sup>



NTNCellConfigStatus defines the observed state of NTNCellConfig.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>appliedKoffset</b></td>
        <td>integer</td>
        <td>
          appliedKoffset is the last successfully applied k-offset value.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntncellconfigstatusconditionsindex">conditions</a></b></td>
        <td>[]object</td>
        <td>
          conditions represent the current state of the resource.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>configMapRef</b></td>
        <td>string</td>
        <td>
          configMapRef is the name of the ConfigMap containing the generated config.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.status.conditions[index]
<sup><sup>[↩ Parent](#ntncellconfigstatus)</sup></sup>



Condition contains details for one aspect of the current state of this API Resource.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>lastTransitionTime</b></td>
        <td>string</td>
        <td>
          lastTransitionTime is the last time the condition transitioned from one status to another.
This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>message</b></td>
        <td>string</td>
        <td>
          message is a human readable message indicating details about the transition.
This may be an empty string.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>reason</b></td>
        <td>string</td>
        <td>
          reason contains a programmatic identifier indicating the reason for the condition's last transition.
Producers of specific condition types may define expected values and meanings for this field,
and whether the values are considered a guaranteed API.
The value should be a CamelCase string.
This field may not be empty.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>status</b></td>
        <td>enum</td>
        <td>
          status of the condition, one of True, False, Unknown.<br/>
          <br/>
            <i>Enum</i>: True, False, Unknown<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>string</td>
        <td>
          type of condition in CamelCase or in foo.example.com/CamelCase.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>observedGeneration</b></td>
        <td>integer</td>
        <td>
          observedGeneration represents the .metadata.generation that the condition was set based upon.
For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date
with respect to the current state of the instance.<br/>
          <br/>
            <i>Format</i>: int64<br/>
            <i>Minimum</i>: 0<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>

## NTNSlice
<sup><sup>[↩ Parent](#ntnoperatorsdevv1alpha1 )</sup></sup>






NTNSlice manages terrestrial-satellite network slice failover,
QoS mapping, and session continuity for NTN enterprise services.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
      <td><b>apiVersion</b></td>
      <td>string</td>
      <td>ntn.operators.dev/v1alpha1</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b>kind</b></td>
      <td>string</td>
      <td>NTNSlice</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta">metadata</a></b></td>
      <td>object</td>
      <td>Refer to the Kubernetes API documentation for the fields of the `metadata` field.</td>
      <td>true</td>
      </tr><tr>
        <td><b><a href="#ntnslicespec">spec</a></b></td>
        <td>object</td>
        <td>
          NTNSliceSpec defines the desired state of an NTN network slice
with terrestrial-satellite failover policy.<br/>
          <br/>
            <i>Validations</i>:<li>self.terrestrialPath.priority == 'primary': terrestrialPath.priority must be 'primary'</li><li>self.satellitePath.priority == 'failover': satellitePath.priority must be 'failover'</li>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#ntnslicestatus">status</a></b></td>
        <td>object</td>
        <td>
          NTNSliceStatus defines the observed state of NTNSlice.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNSlice.spec
<sup><sup>[↩ Parent](#ntnslice)</sup></sup>



NTNSliceSpec defines the desired state of an NTN network slice
with terrestrial-satellite failover policy.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#ntnslicespecfailoverpolicy">failoverPolicy</a></b></td>
        <td>object</td>
        <td>
          failoverPolicy defines when and how to switch between paths.<br/>
          <br/>
            <i>Validations</i>:<li>self.triggers.all(t, t.matches('^ *(rsrp|latency|packetLoss|terrestrialRSRP|terrestrialLatency|terrestrialPacketLoss) *(<=|>=|<|>) *[-+]?([0-9]+([.][0-9]+)?|[.][0-9]+)([eE][-+]?[0-9]+)? *$')): each failoverPolicy.trigger must be 'metric op value' where metric is one of rsrp/latency/packetLoss/terrestrialRSRP/terrestrialLatency/terrestrialPacketLoss, op is one of < <= > >=, and value is a finite number (e.g. 'rsrp < -120')</li>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#ntnslicespecsatellitepath">satellitePath</a></b></td>
        <td>object</td>
        <td>
          satellitePath defines the failover satellite connectivity.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>tenant</b></td>
        <td>string</td>
        <td>
          tenant is the organization or entity that owns this slice.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#ntnslicespecterrestrialpath">terrestrialPath</a></b></td>
        <td>object</td>
        <td>
          terrestrialPath defines the primary terrestrial connectivity.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#ntnslicespecbilling">billing</a></b></td>
        <td>object</td>
        <td>
          billing defines CDR generation parameters.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntnslicespecmetricssource">metricsSource</a></b></td>
        <td>object</td>
        <td>
          metricsSource selects where the failover engine reads path quality
metrics (RSRP, latency, packet loss) from. When omitted, the
controller falls back to annotation-driven simulation for backward
compatibility with existing development deployments.<br/>
          <br/>
            <i>Validations</i>:<li>self.type != 'prometheus' || has(self.prometheus): prometheus block is required when type is 'prometheus'</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntnslicespecqosmapping">qosMapping</a></b></td>
        <td>object</td>
        <td>
          qosMapping defines QoS parameter mapping between paths.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntnslicespecsecurity">security</a></b></td>
        <td>object</td>
        <td>
          security defines handover security requirements.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNSlice.spec.failoverPolicy
<sup><sup>[↩ Parent](#ntnslicespec)</sup></sup>



failoverPolicy defines when and how to switch between paths.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>triggers</b></td>
        <td>[]string</td>
        <td>
          triggers defines conditions that initiate failover (OR logic).
Format: "metric operator value" (e.g., "rsrp < -120").
Validated at admission by the XValidation rule on this type and at runtime by
the failover engine (pkg/slice.ParseTrigger).
Order is intentionally not significant; set merge semantics are desired.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>confirmationSamples</b></td>
        <td>integer</td>
        <td>
          confirmationSamples is the number of CONSECUTIVE reconcile samples on which
the terrestrial triggers must fire before a failover to satellite is taken
(production load balancers such as AWS ALB / GCP count consecutive, not
windowed, probe results). It absorbs a single-sample blip so one noisy
reading does not trip a switch. 1 (the default when unset) preserves the
prior immediate-failover behavior; a value of N delays failover by up to
(N-1) reconcile intervals. The confirmation counter is kept in memory and
resets on any healthy reliable sample; losing it on a controller restart or
leader-election handoff only re-requires confirmation (a DELAY), never causes
a spurious switch.<br/>
          <br/>
            <i>Minimum</i>: 1<br/>
            <i>Maximum</i>: 10<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>hysteresisMargin</b></td>
        <td>string</td>
        <td>
          hysteresisMargin is a dead-band applied to trigger thresholds
during switchback evaluation, preventing flapping when metrics
oscillate near the threshold. The value uses the same unit as
the trigger (dB for RSRP, ms for latency, percent for packetLoss).
Example: with trigger "rsrp < -120" and hysteresisMargin "10",
failover fires at RSRP < -120, but switchback requires RSRP >= -110.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>minTerrestrialDwell</b></td>
        <td>string</td>
        <td>
          minTerrestrialDwell is the minimum time the terrestrial path must be held
after a switchback before another failover to satellite may be taken. It
bounds sub-minute ping-pong after a hand-back. It is a soft, bounded delay
— a genuinely failing terrestrial still fails over once the dwell elapses,
so it never indefinitely blocks a real failover. 0 (the default) disables
it; 30s–120s is a sane range relative to a LEO pass.<br/>
          <br/>
            <i>Format</i>: duration<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>sessionContinuity</b></td>
        <td>boolean</td>
        <td>
          sessionContinuity preserves active sessions during failover.<br/>
          <br/>
            <i>Default</i>: true<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>switchbackDelay</b></td>
        <td>string</td>
        <td>
          switchbackDelay is how long to wait after terrestrial recovers
before switching back (prevents flapping).<br/>
          <br/>
            <i>Format</i>: duration<br/>
            <i>Default</i>: 60s<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNSlice.spec.satellitePath
<sup><sup>[↩ Parent](#ntnslicespec)</sup></sup>



satellitePath defines the failover satellite connectivity.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>ephemerisRef</b></td>
        <td>string</td>
        <td>
          ephemerisRef is the name of the SatelliteEphemeris resource
used to determine satellite pass availability.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>priority</b></td>
        <td>enum</td>
        <td>
          priority is the path priority.<br/>
          <br/>
            <i>Enum</i>: primary, failover<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>provider</b></td>
        <td>string</td>
        <td>
          provider is the network operator name (e.g., "chunghwa-telecom").<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>apn</b></td>
        <td>string</td>
        <td>
          apn is the Access Point Name.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNSlice.spec.terrestrialPath
<sup><sup>[↩ Parent](#ntnslicespec)</sup></sup>



terrestrialPath defines the primary terrestrial connectivity.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>priority</b></td>
        <td>enum</td>
        <td>
          priority is the path priority.<br/>
          <br/>
            <i>Enum</i>: primary, failover<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>provider</b></td>
        <td>string</td>
        <td>
          provider is the network operator name (e.g., "chunghwa-telecom").<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>apn</b></td>
        <td>string</td>
        <td>
          apn is the Access Point Name.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNSlice.spec.billing
<sup><sup>[↩ Parent](#ntnslicespec)</sup></sup>



billing defines CDR generation parameters.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>satelliteRate</b></td>
        <td>enum</td>
        <td>
          satelliteRate is the charging model for satellite path.<br/>
          <br/>
            <i>Enum</i>: per-volume, per-time, per-minute, flat<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>terrestrialRate</b></td>
        <td>enum</td>
        <td>
          terrestrialRate is the charging model for terrestrial path.<br/>
          <br/>
            <i>Enum</i>: per-volume, per-time, flat<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNSlice.spec.metricsSource
<sup><sup>[↩ Parent](#ntnslicespec)</sup></sup>



metricsSource selects where the failover engine reads path quality
metrics (RSRP, latency, packet loss) from. When omitted, the
controller falls back to annotation-driven simulation for backward
compatibility with existing development deployments.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#ntnslicespecmetricssourceprometheus">prometheus</a></b></td>
        <td>object</td>
        <td>
          prometheus configures the Prometheus HTTP API backend.
Required when type is 'prometheus'.<br/>
          <br/>
            <i>Validations</i>:<li>size(self.queries.rsrpDbm) > 0 || size(self.queries.latencyMs) > 0 || size(self.queries.packetLossPercent) > 0: at least one query must be non-empty</li>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>enum</td>
        <td>
          type is the backend kind.<br/>
          <br/>
            <i>Enum</i>: annotations, prometheus<br/>
            <i>Default</i>: annotations<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNSlice.spec.metricsSource.prometheus
<sup><sup>[↩ Parent](#ntnslicespecmetricssource)</sup></sup>



prometheus configures the Prometheus HTTP API backend.
Required when type is 'prometheus'.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>endpoint</b></td>
        <td>string</td>
        <td>
          endpoint is the base URL of the Prometheus HTTP API.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#ntnslicespecmetricssourceprometheusqueries">queries</a></b></td>
        <td>object</td>
        <td>
          queries holds the PromQL expressions for each observable metric.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>queryTimeout</b></td>
        <td>string</td>
        <td>
          queryTimeout limits the wall-clock time spent on each individual
PromQL fetch; the controller issues up to three fetches per
reconcile (one per metric), so the upper bound for a Read is
roughly 3x this value. Defaults to 2s when unset.<br/>
          <br/>
            <i>Format</i>: duration<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNSlice.spec.metricsSource.prometheus.queries
<sup><sup>[↩ Parent](#ntnslicespecmetricssourceprometheus)</sup></sup>



queries holds the PromQL expressions for each observable metric.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>latencyMs</b></td>
        <td>string</td>
        <td>
          latencyMs is a PromQL expression returning a scalar in milliseconds.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>packetLossPercent</b></td>
        <td>string</td>
        <td>
          packetLossPercent is a PromQL expression returning a scalar in
percent (0-100).<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>rsrpDbm</b></td>
        <td>string</td>
        <td>
          rsrpDbm is a PromQL expression returning a scalar in dBm.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNSlice.spec.qosMapping
<sup><sup>[↩ Parent](#ntnslicespec)</sup></sup>



qosMapping defines QoS parameter mapping between paths.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>maxLatencyBudget</b></td>
        <td>string</td>
        <td>
          maxLatencyBudget is the maximum acceptable latency including
satellite propagation delay.<br/>
          <br/>
            <i>Format</i>: duration<br/>
            <i>Default</i>: 150ms<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>satelliteQCI</b></td>
        <td>enum</td>
        <td>
          satelliteQCI is the QoS class for the satellite path.<br/>
          <br/>
            <i>Enum</i>: conversational, streaming, interactive, background, best-effort<br/>
            <i>Default</i>: best-effort<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>terrestrial5QI</b></td>
        <td>integer</td>
        <td>
          terrestrial5QI is the 5G QoS Identifier for the terrestrial path.<br/>
          <br/>
            <i>Minimum</i>: 1<br/>
            <i>Maximum</i>: 255<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNSlice.spec.security
<sup><sup>[↩ Parent](#ntnslicespec)</sup></sup>



security defines handover security requirements.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>authOnHandover</b></td>
        <td>enum</td>
        <td>
          authOnHandover defines authentication behavior during path switch.<br/>
          <br/>
            <i>Enum</i>: re-authenticate, continue<br/>
            <i>Default</i>: re-authenticate<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>encryptionLevel</b></td>
        <td>enum</td>
        <td>
          encryptionLevel specifies the encryption standard.<br/>
          <br/>
            <i>Enum</i>: AES-128, AES-256, SNOW3G, ZUC<br/>
            <i>Default</i>: AES-256<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNSlice.status
<sup><sup>[↩ Parent](#ntnslice)</sup></sup>



NTNSliceStatus defines the observed state of NTNSlice.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>activePathType</b></td>
        <td>enum</td>
        <td>
          activePathType is the currently active network path.<br/>
          <br/>
            <i>Enum</i>: terrestrial, satellite, unavailable<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>appliedEncryption</b></td>
        <td>string</td>
        <td>
          appliedEncryption is the encryption level in effect for the current path.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>appliedQoS</b></td>
        <td>string</td>
        <td>
          appliedQoS summarizes the QoS mapping in effect for the current path.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>billingMode</b></td>
        <td>string</td>
        <td>
          billingMode is the billing model active for the current path.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#ntnslicestatusconditionsindex">conditions</a></b></td>
        <td>[]object</td>
        <td>
          conditions represent the current state of the slice.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>failoverCount</b></td>
        <td>integer</td>
        <td>
          failoverCount is the total number of failover events since creation.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>lastFailover</b></td>
        <td>string</td>
        <td>
          lastFailover is the timestamp of the last failover event.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>sessionCount</b></td>
        <td>integer</td>
        <td>
          sessionCount is the number of active sessions on this slice.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNSlice.status.conditions[index]
<sup><sup>[↩ Parent](#ntnslicestatus)</sup></sup>



Condition contains details for one aspect of the current state of this API Resource.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>lastTransitionTime</b></td>
        <td>string</td>
        <td>
          lastTransitionTime is the last time the condition transitioned from one status to another.
This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>message</b></td>
        <td>string</td>
        <td>
          message is a human readable message indicating details about the transition.
This may be an empty string.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>reason</b></td>
        <td>string</td>
        <td>
          reason contains a programmatic identifier indicating the reason for the condition's last transition.
Producers of specific condition types may define expected values and meanings for this field,
and whether the values are considered a guaranteed API.
The value should be a CamelCase string.
This field may not be empty.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>status</b></td>
        <td>enum</td>
        <td>
          status of the condition, one of True, False, Unknown.<br/>
          <br/>
            <i>Enum</i>: True, False, Unknown<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>string</td>
        <td>
          type of condition in CamelCase or in foo.example.com/CamelCase.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>observedGeneration</b></td>
        <td>integer</td>
        <td>
          observedGeneration represents the .metadata.generation that the condition was set based upon.
For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date
with respect to the current state of the instance.<br/>
          <br/>
            <i>Format</i>: int64<br/>
            <i>Minimum</i>: 0<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>

## SatelliteEphemeris
<sup><sup>[↩ Parent](#ntnoperatorsdevv1alpha1 )</sup></sup>






SatelliteEphemeris manages GP data fetching (OMM JSON from CelesTrak/SpaceTrack),
orbital propagation (SGP4 via akhenakh/sgp4), and pass prediction for a set of
satellites against ground stations.

Orbit-regime support: v1.0 is LEO-only. The propagator is the near-earth SGP4
model; element sets whose orbital period is >= 225 minutes (deep space —
roughly MEO and above, e.g. O3b or GEO) are rejected rather than propagated
into a wrong position, and surface as the UnsupportedOrbitRegime status
condition. Multi-orbit (MEO/GEO) support is a v1.1 roadmap item.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
      <td><b>apiVersion</b></td>
      <td>string</td>
      <td>ntn.operators.dev/v1alpha1</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b>kind</b></td>
      <td>string</td>
      <td>SatelliteEphemeris</td>
      <td>true</td>
      </tr>
      <tr>
      <td><b><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.27/#objectmeta-v1-meta">metadata</a></b></td>
      <td>object</td>
      <td>Refer to the Kubernetes API documentation for the fields of the `metadata` field.</td>
      <td>true</td>
      </tr><tr>
        <td><b><a href="#satelliteephemerisspec">spec</a></b></td>
        <td>object</td>
        <td>
          SatelliteEphemerisSpec defines the desired state of SatelliteEphemeris.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#satelliteephemerisstatus">status</a></b></td>
        <td>object</td>
        <td>
          SatelliteEphemerisStatus defines the observed state of SatelliteEphemeris.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### SatelliteEphemeris.spec
<sup><sup>[↩ Parent](#satelliteephemeris)</sup></sup>



SatelliteEphemerisSpec defines the desired state of SatelliteEphemeris.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#satelliteephemerisspecsource">source</a></b></td>
        <td>object</td>
        <td>
          source defines where to fetch GP (General Perturbations) data.<br/>
          <br/>
            <i>Validations</i>:<li>self.type != 'SpaceTrack' || has(self.credentials): SpaceTrack source type requires credentials (spec.source.credentials)</li>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#satelliteephemerisspecpassprediction">passPrediction</a></b></td>
        <td>object</td>
        <td>
          passPrediction configures automatic pass window computation.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#satelliteephemerisspecsatellites">satellites</a></b></td>
        <td>object</td>
        <td>
          satellites filters which satellites to track from the source.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### SatelliteEphemeris.spec.source
<sup><sup>[↩ Parent](#satelliteephemerisspec)</sup></sup>



source defines where to fetch GP (General Perturbations) data.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>refreshInterval</b></td>
        <td>string</td>
        <td>
          refreshInterval is how often to re-fetch GP data.
CelesTrak updates every 2 hours; setting this below 2h wastes bandwidth.<br/>
          <br/>
            <i>Format</i>: duration<br/>
            <i>Default</i>: 4h<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>enum</td>
        <td>
          type is the source type. Supported: "CelesTrak", "SpaceTrack".<br/>
          <br/>
            <i>Enum</i>: CelesTrak, SpaceTrack<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>url</b></td>
        <td>string</td>
        <td>
          url is the endpoint to fetch GP data from. Use https for any public
source: a cleartext http:// URL that resolves to a public IP is refused
at runtime (InsecureURL condition) because an on-path attacker could
inject forged OMM data that is propagated into SIB19. http:// is permitted
only for a private/in-cluster mirror (NetworkPolicy-protected).
For CelesTrak: https://celestrak.org/NORAD/elements/gp.php?GROUP=oneweb&FORMAT=JSON
For SpaceTrack: https://www.space-track.org/basicspacedata/query/class/gp/...<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b><a href="#satelliteephemerisspecsourcecredentials">credentials</a></b></td>
        <td>object</td>
        <td>
          credentials is a reference to a Secret containing auth credentials
(required for SpaceTrack, optional for CelesTrak).<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### SatelliteEphemeris.spec.source.credentials
<sup><sup>[↩ Parent](#satelliteephemerisspecsource)</sup></sup>



credentials is a reference to a Secret containing auth credentials
(required for SpaceTrack, optional for CelesTrak).

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>name</b></td>
        <td>string</td>
        <td>
          name of the Secret.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>key</b></td>
        <td>string</td>
        <td>
          key within the Secret data.<br/>
          <br/>
            <i>Default</i>: password<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### SatelliteEphemeris.spec.passPrediction
<sup><sup>[↩ Parent](#satelliteephemerisspec)</sup></sup>



passPrediction configures automatic pass window computation.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>groundStations</b></td>
        <td>[]string</td>
        <td>
          groundStations is a list of GroundStationLifecycle resource names
to compute pass windows against.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>horizon</b></td>
        <td>string</td>
        <td>
          horizon is how far into the future to predict passes.<br/>
          <br/>
            <i>Default</i>: 24h<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>minElevation</b></td>
        <td>string</td>
        <td>
          minElevation is the minimum elevation angle in degrees (string, e.g., "10").<br/>
          <br/>
            <i>Default</i>: 10<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### SatelliteEphemeris.spec.satellites
<sup><sup>[↩ Parent](#satelliteephemerisspec)</sup></sup>



satellites filters which satellites to track from the source.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>constellation</b></td>
        <td>string</td>
        <td>
          constellation is DEPRECATED and performs no filtering at all — the controller
has never consumed it (neither server- nor client-side). Select a constellation
in the source URL instead (CelesTrak's GROUP= query parameter, e.g. GROUP=oneweb,
returns only that constellation's element sets) and/or list explicit noradIDs.
It stays accepted in v1alpha1; removal is deferred to a future versioned API
migration — a v1alpha2 that drops it must ship conversion so v1alpha1<->v1alpha2
round-trips losslessly, plus stored-object migration and storedVersions cleanup;
a version rename alone is not enough to safely drop the data.

Deprecated: select the constellation via source.url (GROUP=) or spec.satellites.noradIDs.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>noradIDs</b></td>
        <td>[]integer</td>
        <td>
          noradIDs is an explicit list of NORAD catalog IDs to track.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### SatelliteEphemeris.status
<sup><sup>[↩ Parent](#satelliteephemeris)</sup></sup>



SatelliteEphemerisStatus defines the observed state of SatelliteEphemeris.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#satelliteephemerisstatusconditionsindex">conditions</a></b></td>
        <td>[]object</td>
        <td>
          conditions represent the current state of the resource.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>lastUpdated</b></td>
        <td>string</td>
        <td>
          lastUpdated is when the GP data was last successfully fetched.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#satelliteephemerisstatusnextpasswindowsindex">nextPassWindows</a></b></td>
        <td>[]object</td>
        <td>
          nextPassWindows contains upcoming contact opportunities.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b><a href="#satelliteephemerisstatuspropagatedstatesindex">propagatedStates</a></b></td>
        <td>[]object</td>
        <td>
          propagatedStates holds SGP4-propagated ECEF state vectors (per satellite) at
the last refresh epoch, consumed by NTNCellConfig runtime ephemeris push (#176).
Capped (maxItems) to match the controller's maxPropagatedStates and stay well
under the etcd object-size limit.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>satelliteCount</b></td>
        <td>integer</td>
        <td>
          satelliteCount is the number of satellites currently tracked.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>truncatedSatelliteCount</b></td>
        <td>integer</td>
        <td>
          truncatedSatelliteCount is how many selected satellites were NOT propagated
because the maxPropagatedStates cap (128) had already been reached — the count
actually dropped by the cap, not merely (selected - 128), so satellites that
fail SGP4 propagation do not inflate it. ABSENT or 0 means nothing was dropped
(the field is omitempty). Narrow spec.satellites.noradIDs or the source URL's
GROUP= to eliminate it. Mirrored by the StatesTruncated condition; a Warning
StatesTruncated event fires once per transition into the truncated state.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### SatelliteEphemeris.status.conditions[index]
<sup><sup>[↩ Parent](#satelliteephemerisstatus)</sup></sup>



Condition contains details for one aspect of the current state of this API Resource.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>lastTransitionTime</b></td>
        <td>string</td>
        <td>
          lastTransitionTime is the last time the condition transitioned from one status to another.
This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>message</b></td>
        <td>string</td>
        <td>
          message is a human readable message indicating details about the transition.
This may be an empty string.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>reason</b></td>
        <td>string</td>
        <td>
          reason contains a programmatic identifier indicating the reason for the condition's last transition.
Producers of specific condition types may define expected values and meanings for this field,
and whether the values are considered a guaranteed API.
The value should be a CamelCase string.
This field may not be empty.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>status</b></td>
        <td>enum</td>
        <td>
          status of the condition, one of True, False, Unknown.<br/>
          <br/>
            <i>Enum</i>: True, False, Unknown<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>type</b></td>
        <td>string</td>
        <td>
          type of condition in CamelCase or in foo.example.com/CamelCase.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>observedGeneration</b></td>
        <td>integer</td>
        <td>
          observedGeneration represents the .metadata.generation that the condition was set based upon.
For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date
with respect to the current state of the instance.<br/>
          <br/>
            <i>Format</i>: int64<br/>
            <i>Minimum</i>: 0<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### SatelliteEphemeris.status.nextPassWindows[index]
<sup><sup>[↩ Parent](#satelliteephemerisstatus)</sup></sup>



PassWindow represents a predicted contact opportunity between a satellite and ground station.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>aos</b></td>
        <td>string</td>
        <td>
          aos is the Acquisition of Signal time (satellite rises above minElevation).<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>groundStation</b></td>
        <td>string</td>
        <td>
          groundStation is the name of the GroundStationLifecycle resource.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>los</b></td>
        <td>string</td>
        <td>
          los is the Loss of Signal time (satellite drops below minElevation).<br/>
          <br/>
            <i>Format</i>: date-time<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>maxElevation</b></td>
        <td>string</td>
        <td>
          maxElevation is the peak elevation angle during the pass in degrees (string, e.g., "72.5").<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>satellite</b></td>
        <td>string</td>
        <td>
          satellite is the name or NORAD ID of the satellite.<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### SatelliteEphemeris.status.propagatedStates[index]
<sup><sup>[↩ Parent](#satelliteephemerisstatus)</sup></sup>



PropagatedState is a satellite state vector propagated (SGP4) to a specific
epoch, in the 3GPP ECEF codepoint form the runtime ephemeris push consumes.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b><a href="#satelliteephemerisstatuspropagatedstatesindexecef">ecef</a></b></td>
        <td>object</td>
        <td>
          ecef is the propagated position/velocity in 3GPP codepoints. The provider
converts these to physical SI when pushing to OCUDU.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>epochUnixMs</b></td>
        <td>integer</td>
        <td>
          epochUnixMs is the propagation epoch in Unix milliseconds (in the future,
as OCUDU's ntn_config_update requires).<br/>
          <br/>
            <i>Format</i>: int64<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>noradID</b></td>
        <td>integer</td>
        <td>
          noradID is the satellite's NORAD catalog number, used by NTNCellConfig
(spec.ephemerisNoradID) to select which state to push.<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>satellite</b></td>
        <td>string</td>
        <td>
          satellite is the satellite name or object ID (bounded; the controller
truncates the externally-sourced name to this length).<br/>
        </td>
        <td>true</td>
      </tr></tbody>
</table>


### SatelliteEphemeris.status.propagatedStates[index].ecef
<sup><sup>[↩ Parent](#satelliteephemerisstatuspropagatedstatesindex)</sup></sup>



ecef is the propagated position/velocity in 3GPP codepoints. The provider
converts these to physical SI when pushing to OCUDU.

<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Description</th>
            <th>Required</th>
        </tr>
    </thead>
    <tbody><tr>
        <td><b>posX</b></td>
        <td>integer</td>
        <td>
          posX is the X position in 1.3 m/LSB codepoints (3GPP positionX-r17,
-33554432 to 33554431). The operator emits posX × 1.3 as metres to OCUDU.<br/>
          <br/>
            <i>Minimum</i>: -3.3554432e+07<br/>
            <i>Maximum</i>: 3.355443e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>posY</b></td>
        <td>integer</td>
        <td>
          posY is the Y position in 1.3 m/LSB codepoints (-33554432 to 33554431).<br/>
          <br/>
            <i>Minimum</i>: -3.3554432e+07<br/>
            <i>Maximum</i>: 3.355443e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>posZ</b></td>
        <td>integer</td>
        <td>
          posZ is the Z position in 1.3 m/LSB codepoints (-33554432 to 33554431).<br/>
          <br/>
            <i>Minimum</i>: -3.3554432e+07<br/>
            <i>Maximum</i>: 3.355443e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>velX</b></td>
        <td>integer</td>
        <td>
          velX is the X velocity in 0.06 m/s/LSB codepoints (3GPP velocityVX-r17,
-131072 to 131071; 0 for GEO). The operator emits velX × 0.06 as m/s.<br/>
          <br/>
            <i>Default</i>: 0<br/>
            <i>Minimum</i>: -131072<br/>
            <i>Maximum</i>: 131071<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>velY</b></td>
        <td>integer</td>
        <td>
          velY is the Y velocity in 0.06 m/s/LSB codepoints (-131072 to 131071; 0 for GEO).<br/>
          <br/>
            <i>Default</i>: 0<br/>
            <i>Minimum</i>: -131072<br/>
            <i>Maximum</i>: 131071<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>velZ</b></td>
        <td>integer</td>
        <td>
          velZ is the Z velocity in 0.06 m/s/LSB codepoints (-131072 to 131071; 0 for GEO).<br/>
          <br/>
            <i>Default</i>: 0<br/>
            <i>Minimum</i>: -131072<br/>
            <i>Maximum</i>: 131071<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>
