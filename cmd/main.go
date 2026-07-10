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

package main

import (
	"crypto/tls"
	"flag"
	"os"

	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/internal/controller"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
	"github.com/thc1006/ntn-operators/pkg/netutil"
	"github.com/thc1006/ntn-operators/pkg/provider"
	"github.com/thc1006/ntn-operators/pkg/provider/ocudu"
	slicemetrics "github.com/thc1006/ntn-operators/pkg/slice/metrics"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(ntnv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8443", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or 0 to disable.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. "+
			"Recommended for production to prevent duplicate reconciliations.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics server")
	var maxConcurrentReconciles int
	flag.IntVar(&maxConcurrentReconciles, "max-concurrent-reconciles", 1,
		"Maximum number of concurrent reconciles per controller.")
	var prometheusAllowedHosts string
	flag.StringVar(&prometheusAllowedHosts, "prometheus-allowed-endpoint-hosts", "",
		"Comma-separated list of hostnames that NTNSlice.spec.metricsSource.prometheus.endpoint "+
			"is allowed to target. This gates the hostname AND exempts that host from the "+
			"dial-level SSRF guard. Empty (default) permits any PUBLIC Prometheus but BLOCKS "+
			"dials to private/reserved IPs (cloud metadata, internal ClusterIP services). "+
			"An in-cluster Prometheus at a private ClusterIP must be listed here (by host), "+
			"and multi-tenant clusters should list only the sanctioned endpoints so a tenant "+
			"cannot aim the operator at arbitrary cluster-internal services.")
	var ephemerisAllowedPrivateHosts string
	flag.StringVar(&ephemerisAllowedPrivateHosts, "ephemeris-allowed-private-hosts", "",
		"Comma-separated list of hostnames whose resolved private/reserved IPs are "+
			"permitted for SatelliteEphemeris GP-data fetches. Empty (default) enforces "+
			"the strict SSRF posture: every private IP is blocked at the dial layer. "+
			"Set this only to enable internal CelesTrak mirrors (e.g. air-gapped "+
			"deployments) or E2E test mock servers. Production clusters with public "+
			"CelesTrak access should leave this empty.")
	// Default to the Zap PRODUCTION config (JSON encoder, info level, sampling,
	// error-level stacktraces) rather than the kubebuilder scaffold's Development
	// default (console, warning stacktraces, no sampling), so a deployed operator
	// emits structured logs. Still flag-overridable: `--zap-devel` restores the
	// development config for local `make run`.
	opts := zap.Options{
		Development: false,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	if maxConcurrentReconciles < 1 {
		maxConcurrentReconciles = 1
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "b1076767.operators.dev",
		// Release the lease on graceful shutdown so a standby replica takes over
		// immediately instead of waiting out LeaseDuration (~15s) — the active-passive
		// HA win, and it collapses the rollout dead window. This is SAFE here because
		// main() exits as soon as mgr.Start returns (below) with NO post-Start work:
		// controller-runtime warns (issue #1132) this is only unsafe when the binary
		// keeps running — and performing lease-guarded work — after the Manager stops.
		// Lease timing keeps the controller-runtime/client-go defaults (15s/10s/2s).
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	gpHTTPClient := netutil.NewSafeHTTPClient(
		30*time.Second,
		netutil.WithPrivateHostAllowlist(netutil.ParseEndpointAllowlist(ephemerisAllowedPrivateHosts)),
	)
	spaceTrackFetcher := ephemeris.NewSpaceTrackFetcher(
		netutil.NewSafeHTTPClient(30*time.Second),
		"https://www.space-track.org",
	)
	if err := (&controller.SatelliteEphemerisReconciler{
		Client:                  mgr.GetClient(),
		APIReader:               mgr.GetAPIReader(),
		Scheme:                  mgr.GetScheme(),
		Recorder:                mgr.GetEventRecorder("satelliteephemeris-controller"),
		Fetcher:                 ephemeris.NewCelesTrakFetcher(gpHTTPClient),
		SpaceTrackFetcher:       spaceTrackFetcher,
		MaxConcurrentReconciles: maxConcurrentReconciles,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "SatelliteEphemeris")
		os.Exit(1)
	}
	if err := (&controller.GroundStationLifecycleReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		Recorder:                mgr.GetEventRecorder("groundstationlifecycle-controller"),
		HTTPClient:              netutil.NewSafeHTTPClient(5 * time.Second),
		MaxConcurrentReconciles: maxConcurrentReconciles,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "GroundStationLifecycle")
		os.Exit(1)
	}
	providers := map[string]provider.NTNProvider{
		"ocudu": ocudu.NewProvider(mgr.GetClient()),
	}
	if err := (&controller.NTNCellConfigReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		Recorder:                mgr.GetEventRecorder("ntncellconfig-controller"),
		Providers:               providers,
		MaxConcurrentReconciles: maxConcurrentReconciles,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "NTNCellConfig")
		os.Exit(1)
	}
	// Shared across NTNSlice reconciles: one pool of Prometheus clients
	// keyed by endpoint, and one Provider that picks the right Reader
	// (annotations | prometheus) based on each NTNSlice spec. The same
	// allowlist serves both layers: the Provider's hostname gate (permit-all
	// when empty) and the pool's SSRF-safe dialer's private-host bypass
	// (permit-nothing when empty). So by default the operator reaches any
	// PUBLIC Prometheus but is blocked from dialing private/reserved IPs
	// (cloud metadata, internal ClusterIP services) — an in-cluster Prometheus
	// must be allow-listed by host (findings.md I-24).
	promAllow := netutil.ParseEndpointAllowlist(prometheusAllowedHosts)
	metricsPool := slicemetrics.NewClientPool(slicemetrics.WithSafeClient(promAllow))
	metricsProvider := slicemetrics.NewProvider(metricsPool,
		slicemetrics.WithEndpointAllowlist(promAllow))
	if err := (&controller.NTNSliceReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		Recorder:                mgr.GetEventRecorder("ntnslice-controller"),
		MaxConcurrentReconciles: maxConcurrentReconciles,
		ReaderProvider:          metricsProvider,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "NTNSlice")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	// NTNSlice failover-trigger syntax is validated at admission by the CEL
	// x-kubernetes-validations rule on the CRD (api/v1alpha1), so no validating
	// webhook (and no cert-manager dependency) is needed — see findings.md B-2.

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	// readyz is intentionally leadership-agnostic (healthz.Ping): under active-passive
	// HA a NON-leader standby must still report Ready, or the Deployment never counts
	// it Available and a rolling update deadlocks (the new pod can only become leader
	// after the old releases the lease, but the old is not torn down until the new is
	// Ready). A cache-sync- or leadership-gated readyz would break HA the same way,
	// because a non-leader's cache/controllers do not start until it acquires the lease.
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
