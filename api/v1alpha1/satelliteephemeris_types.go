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

// EphemerisSource defines where to fetch TLE/ephemeris data from.
type EphemerisSource struct {
	// type is the source type. Supported: "CelesTrak", "SpaceTrack".
	// +kubebuilder:validation:Enum=CelesTrak;SpaceTrack
	Type string `json:"type"`

	// url is the endpoint to fetch TLE data from.
	// For CelesTrak: https://celestrak.org/NORAD/elements/gp.php?GROUP=oneweb
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// refreshInterval is how often to re-fetch TLE data.
	// +kubebuilder:default="4h"
	RefreshInterval metav1.Duration `json:"refreshInterval"`

	// credentials is a reference to a Secret containing auth credentials
	// (required for SpaceTrack, optional for CelesTrak).
	// +optional
	Credentials *SecretReference `json:"credentials,omitempty"`
}

// SecretReference points to a Kubernetes Secret.
type SecretReference struct {
	// name of the Secret.
	Name string `json:"name"`
	// key within the Secret data.
	// +kubebuilder:default="password"
	Key string `json:"key,omitempty"`
}

// SatelliteSelector defines which satellites to track.
type SatelliteSelector struct {
	// constellation filters by constellation name (e.g., "oneweb", "starlink").
	// +optional
	Constellation string `json:"constellation,omitempty"`

	// noradIDs is an explicit list of NORAD catalog IDs to track.
	// +optional
	NoradIDs []int `json:"noradIDs,omitempty"`
}

// PassPredictionSpec configures pass prediction calculations.
type PassPredictionSpec struct {
	// groundStations is a list of GroundStationLifecycle resource names
	// to compute pass windows against.
	// +kubebuilder:validation:MinItems=1
	GroundStations []string `json:"groundStations"`

	// minElevation is the minimum elevation angle in degrees (string, e.g., "10").
	// +kubebuilder:default="10"
	MinElevation string `json:"minElevation,omitempty"`

	// horizon is how far into the future to predict passes.
	// +kubebuilder:default="24h"
	Horizon metav1.Duration `json:"horizon,omitempty"`
}

// SatelliteEphemerisSpec defines the desired state of SatelliteEphemeris.
type SatelliteEphemerisSpec struct {
	// source defines where to fetch TLE/ephemeris data.
	// +required
	Source EphemerisSource `json:"source"`

	// satellites filters which satellites to track from the source.
	// +optional
	Satellites *SatelliteSelector `json:"satellites,omitempty"`

	// passPrediction configures automatic pass window computation.
	// +optional
	PassPrediction *PassPredictionSpec `json:"passPrediction,omitempty"`
}

// PassWindow represents a predicted contact opportunity between a satellite and ground station.
type PassWindow struct {
	// satellite is the name or NORAD ID of the satellite.
	Satellite string `json:"satellite"`

	// groundStation is the name of the GroundStationLifecycle resource.
	GroundStation string `json:"groundStation"`

	// aos is the Acquisition of Signal time (satellite rises above minElevation).
	AOS metav1.Time `json:"aos"`

	// los is the Loss of Signal time (satellite drops below minElevation).
	LOS metav1.Time `json:"los"`

	// maxElevation is the peak elevation angle during the pass in degrees (string, e.g., "72.5").
	MaxElevation string `json:"maxElevation"`
}

// SatelliteEphemerisStatus defines the observed state of SatelliteEphemeris.
type SatelliteEphemerisStatus struct {
	// lastUpdated is when the TLE data was last successfully fetched.
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`

	// satelliteCount is the number of satellites currently tracked.
	// +optional
	SatelliteCount int `json:"satelliteCount,omitempty"`

	// nextPassWindows contains upcoming contact opportunities.
	// +optional
	NextPassWindows []PassWindow `json:"nextPassWindows,omitempty"`

	// conditions represent the current state of the resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Satellites",type=integer,JSONPath=`.status.satelliteCount`
// +kubebuilder:printcolumn:name="Last Updated",type=date,JSONPath=`.status.lastUpdated`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SatelliteEphemeris manages TLE fetching, orbital propagation, and
// pass prediction for a set of satellites against ground stations.
type SatelliteEphemeris struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec SatelliteEphemerisSpec `json:"spec"`

	// +optional
	Status SatelliteEphemerisStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SatelliteEphemerisList contains a list of SatelliteEphemeris.
type SatelliteEphemerisList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SatelliteEphemeris `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SatelliteEphemeris{}, &SatelliteEphemerisList{})
}
