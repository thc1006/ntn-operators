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

// HardwareSpec describes the ground station hardware.
type HardwareSpec struct {
	// vendor is the hardware manufacturer (e.g., "ennoconn").
	// +kubebuilder:validation:MinLength=1
	Vendor string `json:"vendor"`

	// model is the hardware model identifier.
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`

	// antennaType is the antenna type (e.g., "flat-panel", "parabolic").
	// +optional
	AntennaType string `json:"antennaType,omitempty"`

	// bands lists the supported frequency bands (e.g., ["Ka", "Ku", "S"]).
	// +optional
	Bands []string `json:"bands,omitempty"`
}

// GeoLocation defines a geographic position.
// Coordinates are strings to avoid float serialization issues across languages.
// Example: lat: "25.0330", lon: "121.5654", alt: "15"
// +kubebuilder:validation:XValidation:rule="double(self.lat) >= -90.0 && double(self.lat) <= 90.0",message="lat must be between -90 and 90"
// +kubebuilder:validation:XValidation:rule="double(self.lon) >= -180.0 && double(self.lon) <= 180.0",message="lon must be between -180 and 180"
type GeoLocation struct {
	// lat is the latitude in decimal degrees (string, e.g., "25.0330").
	// +kubebuilder:validation:Pattern=`^-?[0-9]+\.?[0-9]*$`
	Lat string `json:"lat"`

	// lon is the longitude in decimal degrees (string, e.g., "121.5654").
	// +kubebuilder:validation:Pattern=`^-?[0-9]+\.?[0-9]*$`
	Lon string `json:"lon"`

	// alt is the altitude in meters above sea level (string, e.g., "15").
	// +optional
	Alt string `json:"alt,omitempty"`
}

// DeploymentSpec defines the ground station deployment configuration.
type DeploymentSpec struct {
	// location is the geographic position of the ground station.
	// +required
	Location GeoLocation `json:"location"`

	// k8sDistro is the Kubernetes distribution running on the edge box.
	// +kubebuilder:validation:Enum=k3s;microk8s;rke2
	// +kubebuilder:default="k3s"
	K8sDistro string `json:"k8sDistro,omitempty"`

	// gitopsRepo is the Git repository URL for GitOps-managed configuration.
	// +optional
	GitopsRepo string `json:"gitopsRepo,omitempty"`
}

// MonitoringSpec defines health monitoring parameters.
type MonitoringSpec struct {
	// healthCheckInterval is how often to check ground station health.
	// +kubebuilder:default="30s"
	HealthCheckInterval metav1.Duration `json:"healthCheckInterval,omitempty"`

	// endpoint is the monitoring endpoint of the ground station agent.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
}

// FirmwareSpec defines OTA firmware management.
type FirmwareSpec struct {
	// autoUpdate enables automatic firmware updates.
	// +kubebuilder:default=false
	AutoUpdate bool `json:"autoUpdate,omitempty"`

	// channel is the firmware update channel (e.g., "stable", "beta").
	// +kubebuilder:default="stable"
	Channel string `json:"channel,omitempty"`

	// maintenanceWindow is the time window for updates (e.g., "02:00-04:00 UTC").
	// +optional
	MaintenanceWindow string `json:"maintenanceWindow,omitempty"`
}

// GroundStationLifecycleSpec defines the desired state of a ground station.
type GroundStationLifecycleSpec struct {
	// hardware describes the ground station equipment.
	// +required
	Hardware HardwareSpec `json:"hardware"`

	// deployment defines the station deployment and location.
	// +required
	Deployment DeploymentSpec `json:"deployment"`

	// monitoring defines health check parameters.
	// +optional
	Monitoring *MonitoringSpec `json:"monitoring,omitempty"`

	// firmware defines OTA update configuration.
	// +optional
	Firmware *FirmwareSpec `json:"firmware,omitempty"`
}

// GroundStationPhase represents the lifecycle phase of a ground station.
// +kubebuilder:validation:Enum=Provisioning;Running;Degraded;Offline;Updating
type GroundStationPhase string

const (
	PhaseProvisioning GroundStationPhase = "Provisioning"
	PhaseRunning      GroundStationPhase = "Running"
	PhaseDegraded     GroundStationPhase = "Degraded"
	PhaseOffline      GroundStationPhase = "Offline"
	PhaseUpdating     GroundStationPhase = "Updating"
)

// GroundStationLifecycleStatus defines the observed state of a ground station.
type GroundStationLifecycleStatus struct {
	// phase is the current lifecycle phase.
	// +optional
	Phase GroundStationPhase `json:"phase,omitempty"`

	// k8sVersion is the Kubernetes version running on the edge node.
	// +optional
	K8sVersion string `json:"k8sVersion,omitempty"`

	// firmwareVersion is the currently running firmware version.
	// +optional
	FirmwareVersion string `json:"firmwareVersion,omitempty"`

	// lastHealthCheck is the timestamp of the last successful health check.
	// +optional
	LastHealthCheck *metav1.Time `json:"lastHealthCheck,omitempty"`

	// firmwareUpdateStarted is when the current firmware update began.
	// Used for timeout detection.
	// +optional
	FirmwareUpdateStarted *metav1.Time `json:"firmwareUpdateStarted,omitempty"`

	// conditions represent the current state of the ground station.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for GroundStationLifecycle.
const (
	ConditionAntennaReady     = "AntennaReady"
	ConditionRFLinkHealthy    = "RFLinkHealthy"
	ConditionK8sNodeReady     = "K8sNodeReady"
	ConditionFirmwareUpToDate = "FirmwareUpToDate"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=gs
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Vendor",type=string,JSONPath=`.spec.hardware.vendor`
// +kubebuilder:printcolumn:name="K8s",type=string,JSONPath=`.status.k8sVersion`
// +kubebuilder:printcolumn:name="Last Check",type=date,JSONPath=`.status.lastHealthCheck`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GroundStationLifecycle manages the lifecycle of a satellite ground station,
// including health monitoring, firmware OTA, and GitOps-based configuration.
type GroundStationLifecycle struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec GroundStationLifecycleSpec `json:"spec"`

	// +optional
	Status GroundStationLifecycleStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GroundStationLifecycleList contains a list of GroundStationLifecycle.
type GroundStationLifecycleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GroundStationLifecycle `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GroundStationLifecycle{}, &GroundStationLifecycleList{})
}
