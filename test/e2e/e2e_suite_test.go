//go:build e2e
// +build e2e

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

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/thc1006/ntn-operators/test/utils"
)

var (
	// managerImage is the manager image to be built and loaded for testing.
	// Override with the MANAGER_IMAGE env var when targeting an existing
	// cluster so the controller Deployment pulls from a reachable registry.
	managerImage = func() string {
		if v := os.Getenv("MANAGER_IMAGE"); v != "" {
			return v
		}
		return "example.com/ntn-operators:v0.0.1"
	}()
	// shouldCleanupCertManager tracks whether CertManager was installed by this suite.
	shouldCleanupCertManager = false
)

// celestrakMockHost is the in-cluster DNS name the mock server answers on.
// Must match the Service FQDN in test/e2e/fixtures/celestrak-mock.yaml AND
// the URL in test/e2e/fixtures/satelliteephemeris-e2e.yaml.
const celestrakMockHost = "celestrak-mock.default.svc.cluster.local"

// TestE2E runs the e2e test suite to validate the solution in an isolated environment.
// The default setup requires Kind and CertManager.
//
// To skip CertManager installation, set: CERT_MANAGER_INSTALL_SKIP=true
// To run against a pre-existing cluster (e.g. kubeadm) with the manager
// image already pushed to a registry the cluster can pull from, set:
//   E2E_USE_EXISTING_CLUSTER=1 MANAGER_IMAGE=<your-registry>/ntn-operators:v0.0.1
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting ntn-operators e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	if os.Getenv("E2E_USE_EXISTING_CLUSTER") == "" {
		By("building the manager image")
		cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", managerImage))
		_, err := utils.Run(cmd)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager image")

		By("loading the manager image on Kind")
		err = utils.LoadImageToKindClusterWithName(managerImage)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager image into Kind")
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "E2E_USE_EXISTING_CLUSTER=1 — skipping docker-build / Kind load (image expected at %s)\n", managerImage)
	}

	setupCertManager()
	setupCelestrakMock()
})

var _ = AfterSuite(func() {
	teardownCelestrakMock()
	teardownCertManager()
})

// setupCelestrakMock deploys the in-cluster mock that stands in for
// celestrak.org during E2E. The mock is a tiny nginx Pod serving a
// checked-in OMM JSON fixture — the operator fetches from its Service,
// eliminating CelesTrak network flake from the E2E job. The operator
// gets the --ephemeris-allowed-private-hosts flag in its BeforeAll so
// its SSRF-safe client can dial the Service's private IP.
//
// See pkg/ephemeris/testdata/oneweb-gp.json (the fixture) and
// test/e2e/fixtures/{celestrak-mock.yaml,satelliteephemeris-e2e.yaml}.
func setupCelestrakMock() {
	By("creating celestrak-mock ConfigMap from pkg/ephemeris/testdata/oneweb-gp.json")
	// Server-side-apply style: regenerate ConfigMap manifest from the
	// fixture file at test-runtime. This is the SAME file that
	// pkg/ephemeris's unit tests embed via
	// `//go:embed testdata/oneweb-gp.json`, so fetcher behaviour is
	// validated against identical bytes at both the unit and E2E layers.
	cmd := exec.Command("kubectl", "create", "configmap", "celestrak-mock-fixture",
		"-n", "default",
		"--from-file=gp.json=pkg/ephemeris/testdata/oneweb-gp.json",
		"--dry-run=client", "-o", "yaml")
	cfgMapYAML, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build ConfigMap manifest")
	applyCmd := exec.Command("kubectl", "apply", "-f", "-")
	applyCmd.Stdin = strings.NewReader(cfgMapYAML)
	_, err = utils.Run(applyCmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to apply celestrak-mock ConfigMap")

	By("applying celestrak-mock Deployment + Service")
	cmd = exec.Command("kubectl", "apply", "-f", "test/e2e/fixtures/celestrak-mock.yaml")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to apply celestrak-mock manifests")

	By("waiting for celestrak-mock Deployment to become available")
	cmd = exec.Command("kubectl", "wait", "--for=condition=available",
		"--timeout=60s", "deployment/celestrak-mock", "-n", "default")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "celestrak-mock Deployment did not become available")
}

// teardownCelestrakMock best-effort removes the mock at suite end.
// Errors are ignored — the Kind cluster itself is torn down after the
// suite and clean-up failures here should not fail the run.
func teardownCelestrakMock() {
	By("deleting celestrak-mock resources")
	cmd := exec.Command("kubectl", "delete", "-f", "test/e2e/fixtures/celestrak-mock.yaml",
		"--ignore-not-found", "--timeout=30s")
	_, _ = utils.Run(cmd)
	cmd = exec.Command("kubectl", "delete", "configmap", "celestrak-mock-fixture",
		"-n", "default", "--ignore-not-found", "--timeout=30s")
	_, _ = utils.Run(cmd)
}

// setupCertManager installs CertManager if needed for webhook tests.
// Skips installation if CERT_MANAGER_INSTALL_SKIP=true or if already present.
func setupCertManager() {
	if os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true" {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager installation (CERT_MANAGER_INSTALL_SKIP=true)\n")
		return
	}

	By("checking if CertManager is already installed")
	if utils.IsCertManagerCRDsInstalled() {
		_, _ = fmt.Fprintf(GinkgoWriter, "CertManager is already installed. Skipping installation.\n")
		return
	}

	// Mark for cleanup before installation to handle interruptions and partial installs.
	shouldCleanupCertManager = true

	By("installing CertManager")
	Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
}

// teardownCertManager uninstalls CertManager if it was installed by setupCertManager.
// This ensures we only remove what we installed.
func teardownCertManager() {
	if !shouldCleanupCertManager {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager cleanup (not installed by this suite)\n")
		return
	}

	By("uninstalling CertManager")
	utils.UninstallCertManager()
}
