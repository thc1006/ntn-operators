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
        <td><b><a href="#ntncellconfigspeccelloverrides">cellOverrides</a></b></td>
        <td>object</td>
        <td>
          cellOverrides allows fine-tuning PUCCH, PDSCH, PRACH, and RRC parameters.<br/>
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
          cellSpecificKoffset sets the cell-specific k-offset for NTN (0-1023).<br/>
          <br/>
            <i>Default</i>: 150<br/>
            <i>Minimum</i>: 0<br/>
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
            <i>Maximum</i>: 6.6485757e+07<br/>
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
          posX is the X position of the satellite (-67108864 to 67108863).<br/>
          <br/>
            <i>Minimum</i>: -6.7108864e+07<br/>
            <i>Maximum</i>: 6.7108863e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>posY</b></td>
        <td>integer</td>
        <td>
          posY is the Y position of the satellite (-67108864 to 67108863).<br/>
          <br/>
            <i>Minimum</i>: -6.7108864e+07<br/>
            <i>Maximum</i>: 6.7108863e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>posZ</b></td>
        <td>integer</td>
        <td>
          posZ is the Z position of the satellite (-67108864 to 67108863).<br/>
          <br/>
            <i>Minimum</i>: -6.7108864e+07<br/>
            <i>Maximum</i>: 6.7108863e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>velX</b></td>
        <td>integer</td>
        <td>
          velX is the X velocity of the satellite (0 for GEO).<br/>
          <br/>
            <i>Default</i>: 0<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>velY</b></td>
        <td>integer</td>
        <td>
          velY is the Y velocity of the satellite (0 for GEO).<br/>
          <br/>
            <i>Default</i>: 0<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>velZ</b></td>
        <td>integer</td>
        <td>
          velZ is the Z velocity of the satellite (0 for GEO).<br/>
          <br/>
            <i>Default</i>: 0<br/>
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
          eccentricity is the orbital eccentricity scaled by 1e6 (0-999999 for e < 1.0).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 999999<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>inclination</b></td>
        <td>integer</td>
        <td>
          inclination is the orbital inclination in 1e-4 degrees (0-1800000 = 0°-180°).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 1.8e+06<br/>
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
          semiMajorAxis is the semi-major axis in metres.<br/>
          <br/>
            <i>Minimum</i>: 6.37e+06<br/>
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
        <td><b>t304</b></td>
        <td>integer</td>
        <td>
          t304 is the handover timer value in milliseconds per 3GPP TS 38.331.<br/>
          <br/>
            <i>Enum</i>: 50, 100, 150, 200, 500, 1000, 2000, 10000<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>targetPCI</b></td>
        <td>integer</td>
        <td>
          targetPCI is the Physical Cell Identity of the target cell after switch (0-1007).<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
            <i>Maximum</i>: 1007<br/>
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
            <i>Maximum</i>: 6.6485757e+07<br/>
        </td>
        <td>true</td>
      </tr><tr>
        <td><b>taCommonDrift</b></td>
        <td>integer</td>
        <td>
          taCommonDrift is the TA drift rate.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>taCommonDriftVariant</b></td>
        <td>integer</td>
        <td>
          taCommonDriftVariant is the TA drift rate variant.<br/>
        </td>
        <td>false</td>
      </tr><tr>
        <td><b>taCommonOffset</b></td>
        <td>integer</td>
        <td>
          taCommonOffset is an additional TA offset.<br/>
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
siWindowPosition=1). Tune when PDCCH capacity is tight or when
SIB19 broadcast cadence needs to track short ntn-UlSyncValidityDur.<br/>
        </td>
        <td>false</td>
      </tr></tbody>
</table>


### NTNCellConfig.spec.cellOverrides.sibSchedule
<sup><sup>[↩ Parent](#ntncellconfigspeccelloverrides)</sup></sup>



sibSchedule tunes SIB19 broadcast scheduling. Any unset sub-field
falls back to the defaults (siWindowLength=5, siPeriod=16,
siWindowPosition=1). Tune when PDCCH capacity is tight or when
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
          siWindowPosition is the slot offset within the SI period. Adjust
to avoid collision with SIB1/SIB2 scheduling windows. Pointer so 0
(the first slot) can be distinguished from unset.<br/>
          <br/>
            <i>Minimum</i>: 0<br/>
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
Validated at runtime by the failover engine (pkg/slice.ParseTrigger).
Order is intentionally not significant; set merge semantics are desired.<br/>
        </td>
        <td>true</td>
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
          url is the endpoint to fetch GP data from.
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
          constellation filters by constellation name (e.g., "oneweb", "starlink").<br/>
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
        <td><b>satelliteCount</b></td>
        <td>integer</td>
        <td>
          satelliteCount is the number of satellites currently tracked.<br/>
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
