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
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"net/http"
	"os"
	"sync/atomic"
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

// gracefulShutdownTimeout is NEGATIVE on purpose: controller-runtime treats a negative value as "wait
// indefinitely for the runnables to stop." This is the only SAFE pairing with the
// LeaderElectionReleaseOnCancel below. The manager releases the lease in a defer that runs only AFTER
// the runnable-stop wait returns (manager/internal.go engageStopProcedure). With a FINITE timeout,
// runnableGroup.StopAndWait returns on ctx.Done() WITHOUT the runnable WaitGroup draining, so a timeout
// shorter than the pod's terminationGracePeriodSeconds would let the lease be released while a reconcile
// is still doing lease-guarded work — a split-brain window (controller-runtime #1132). Waiting
// indefinitely means the lease is released ONLY after the runnables have truly stopped; if a runnable
// hangs, the kubelet SIGKILLs the pod at terminationGracePeriodSeconds WITHOUT the release defer running,
// so failover safely degrades to waiting out LeaseDuration rather than risking two active leaders.
// terminationGracePeriodSeconds (30s in the manifests) gives the release HEADROOM when the runnables stop
// promptly — the release is a Get+Update bounded by RenewDeadline — but it is NOT a guarantee: if the
// grace left after the runnables stop is too short, the kubelet SIGKILL still falls back safely to
// lease-expiry failover. The wiring is set by newManagerOptions and pinned by TestNewManagerOptionsAreSafe.
const gracefulShutdownTimeout = -1 * time.Second

// Leader-election timing, set EXPLICITLY (not left to the controller-runtime defaults) so the values are
// pinned against a dependency bump and shared with the tests that reason about shutdown timing. These are
// the current controller-runtime/client-go defaults.
const (
	leaderLeaseDuration = 15 * time.Second
	leaderRenewDeadline = 10 * time.Second
	leaderRetryPeriod   = 2 * time.Second
)

// newManagerOptions builds the manager Options, including the leader-election and graceful-shutdown fields
// that make active-passive HA safe. It is a single constructor (not a mutating helper) so main() must use
// its return value — deleting the call fails to compile — and TestNewManagerOptionsAreSafe can assert the
// wiring without starting a manager. Dropping the negative GracefulShutdownTimeout reintroduces the
// early-release safety risk (#1132); dropping ReleaseOnCancel or the explicit lease timing only degrades
// failover speed or removes dependency-upgrade pinning (the values match the controller-runtime defaults).
func newManagerOptions(
	scheme *runtime.Scheme, metrics metricsserver.Options, probeAddr string, leaderElection bool,
) ctrl.Options {
	gracefulShutdown := gracefulShutdownTimeout // addressable copies for the *time.Duration options
	leaseDuration := leaderLeaseDuration
	renewDeadline := leaderRenewDeadline
	retryPeriod := leaderRetryPeriod
	return ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metrics,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElection,
		LeaderElectionID:       "b1076767.operators.dev",
		// Release the lease on graceful shutdown so a standby takes over immediately instead of waiting out
		// LeaseDuration — the active-passive HA win. SAFE only with the negative GracefulShutdownTimeout
		// below, and because main() exits promptly after mgr.Start() returns with no lease-guarded work (#1132).
		LeaderElectionReleaseOnCancel: true,
		// Wait indefinitely for runnables before cancelling leader election. The manager releases the lease
		// in a defer that runs only AFTER the runnable-stop wait returns; a FINITE timeout would let that
		// wait return on ctx-expiry (runnableGroup.StopAndWait) while a reconcile is still doing lease-guarded
		// work, and release the lease then — a split-brain window. Waiting indefinitely releases only after
		// the runnables truly stop; if shutdown outlasts terminationGracePeriodSeconds the kubelet kills the
		// process and the lease expires naturally.
		GracefulShutdownTimeout: &gracefulShutdown,
		LeaseDuration:           &leaseDuration,
		RenewDeadline:           &renewDeadline,
		RetryPeriod:             &retryPeriod,
	}
}

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

	mgrOpts := newManagerOptions(scheme, metricsServerOptions, probeAddr, enableLeaderElection)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
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
		// WithAPIReader wires the uncached reader so ownership checks and the status
		// read-back see a just-created ConfigMap immediately, instead of racing the
		// eventually-consistent informer cache (which would persist an empty
		// configMapRef on the first reconcile and only self-heal on the 5m requeue).
		"ocudu": ocudu.NewProvider(mgr.GetClient()).WithAPIReader(mgr.GetAPIReader()),
	}
	if err := (&controller.NTNCellConfigReconciler{
		Client:                  mgr.GetClient(),
		APIReader:               mgr.GetAPIReader(),
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
	// cachesSynced flips true once the manager's informer caches have synced; the
	// runnable that sets it is non-leader-election, so it runs on EVERY replica
	// (see cacheReadyRunnable). Register it before the readyz check that reads it.
	var cachesSynced atomic.Bool
	if err := mgr.Add(cacheReadyRunnable{ready: &cachesSynced}); err != nil {
		setupLog.Error(err, "Failed to register cache-sync readiness runnable")
		os.Exit(1)
	}
	// /readyz gates on informer cache-sync but NOT on leadership. controller-runtime
	// starts the health server BEFORE the caches sync, so a bare healthz.Ping readyz
	// reports Ready before (or without ever) syncing — a broken new replica would then
	// be counted Available and the rollout would tear down the healthy old ones. Gating
	// on cache-sync closes that. It stays leadership-AGNOSTIC on purpose: every replica
	// syncs its cache before leader election (v0.24.1 internal.go: caches → non-leader
	// runnables → leader election), so the standby becomes Ready without holding the
	// lease. Gating on leadership INSTEAD would deadlock rollouts — the new pod can't win
	// the lease until the old releases it, but the old is not removed until the new is Ready.
	if err := mgr.AddReadyzCheck("readyz", cacheSyncReadyCheck(&cachesSynced)); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

// cacheReadyRunnable flips its flag true once the manager's caches have synced.
// controller-runtime starts non-leader-election runnables only AFTER the caches
// sync, and on EVERY replica regardless of leadership (v0.24.1 internal.go order:
// HTTPServers → Webhooks → Caches(wait for sync) → non-leader runnables → leader
// election). So Start running means the informer caches are synced — which is what
// lets /readyz gate on cache-sync without gating on leadership.
type cacheReadyRunnable struct {
	ready *atomic.Bool
}

func (c cacheReadyRunnable) Start(ctx context.Context) error {
	c.ready.Store(true)
	<-ctx.Done()
	return nil
}

// NeedLeaderElection must return false: a leader-election runnable runs only on the
// elected leader, so a standby's flag would never flip and its /readyz would never
// pass — deadlocking rollouts. false makes this a non-leader runnable that starts on
// every replica once caches have synced.
func (cacheReadyRunnable) NeedLeaderElection() bool { return false }

// cacheSyncReadyCheck returns a readyz Checker that passes only once ready is set
// (i.e. the manager's caches have synced). Leadership is deliberately not consulted.
func cacheSyncReadyCheck(ready *atomic.Bool) healthz.Checker {
	return func(_ *http.Request) error {
		if ready.Load() {
			return nil
		}
		return errors.New("informer caches not yet synced")
	}
}
