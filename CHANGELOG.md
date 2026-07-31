# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security

- **Closed a residual Secret existence/type oracle in the `remoteControl.tls` credential path.** #219
  unified the CR-facing *message* for a `remoteControl.tls` resolution failure but not the *reason*: a
  missing/unreadable Secret classified as `ProviderPushFailed` (1 min requeue) while a present-but-bad
  one (wrong type / missing opt-in label / malformed cert) classified as `RemoteControlConfigInvalid`
  (5 min requeue). Since the controller writes that reason verbatim into
  `status.conditions[].reason` — and stamps it on the per-failure `EphemerisPushErrorsTotal` metric — a
  principal who can write an `NTNCellConfig` but has no `secrets/get` could still distinguish "my
  guessed Secret exists but isn't a valid credential" from "it is missing/unreadable", defeating #219's
  "No Secret existence/type oracle" claim. Every `remoteControl.tls` failure now surfaces the single
  uniform reason `RemoteControlCredentialUnavailable` with the uniform message **and** the same 5 min
  self-heal cadence, so neither the condition, the metric label, nor the metric's per-failure increment
  *rate* (a channel the message/reason split alone left open) can tell the cases apart. The specific
  cause is still logged for the operator (who can read Secrets); genuinely transient errors still
  recover fast via the ~3 min ephemeris heartbeat. A table-driven regression pins that all seven
  failure modes (missing, unreadable, unlabelled, service-account token, bootstrap token, malformed CA,
  malformed client cert) yield an identical public reason, message, and requeue interval. The sibling
  endpoint allow-list (`--remote-control-allowed-endpoint-hosts`) keeps its own distinct reason
  `RemoteControlEndpointNotAllowed`: it fires before the Secret read and reflects admin egress policy,
  not Secret state, so folding it into the uniform credential reason would be inaccurate and needless.

### Added

- **`ntn_operators_config_apply_ready` — the config-apply half of #216, which shipped only the
  push half.** `ntn_operators_config_apply_errors_total` is incremented **only** by an
  `ApplyCellConfig` failure, so three other ways the apply can be broken incremented it **zero**
  times *and* returned a nil error — making them invisible to `NTNConfigApplyErrors` (a rate alert
  on that counter) **and** to `controller_runtime_reconcile_errors_total`: `InternalError` (the
  provider registry is not configured), `UnsupportedProvider` (`spec.provider.type` unregistered,
  which additionally does **not requeue** — exactly the permanent case a rate alert can never
  sustain on), and `StatusCheckFailed` (the write may have landed but the post-apply read could not
  verify it). The new gauge is 1 only on a verified apply and 0 on all four failure paths, holds its
  value across scrapes regardless of requeue, and is released on CR deletion. Alert
  **`NTNConfigApplyNotReady`** (`== 0` for 15m) and a runbook section keyed by the `ConfigApplied`
  reason ship with it. Keyed `namespace`+`config` like `ephemeris_push_ready`, deliberately without
  the counter's `provider` label so a provider-type edit cannot strand a stale series at 0.

- **Durable conditional-GET validators for the OMM cache (polite restart/failover refetch).** The
  CelesTrak fetcher already used an in-memory `If-None-Match` conditional GET, but the ETag was lost on
  a process restart or leader failover, so the first post-failover fetch re-downloaded the full GP body.
  The last-good OMM cache ConfigMap (ADR-0007) now also persists the origin's `ETag` and `Last-Modified`
  validators; on cold-start restore they are re-seeded into the fetcher (`SeedConditionalCache`) so the
  first fetch this process makes is a conditional GET — a `304 Not Modified` when the data is unchanged,
  which is politer to CelesTrak (Dr. Kelso's usage policy) and cheaper. Only the validators are
  re-seeded, never the body: the fetcher's OMM cache is keyed by URL and shared across every CR fetching
  that URL, whereas a durable entry holds only that CR's filtered/capped subset. A resulting cold-start
  `304` therefore returns no body, and the reconciler re-serves the restoring CR's own cache — continuity
  (`status.propagatedStates` keeps advancing) is preserved with the `NotModified` status semantics
  intact. `If-Modified-Since` is also sent when only a `Last-Modified` validator is available, so an
  origin that emits no `ETag` can still answer `304`. CelesTrak-only; Space-Track is unaffected.

### Security

- **External OMM `OBJECT_NAME` can no longer amplify the SatelliteEphemeris status past the etcd object
  limit.** `PropagatedState.satellite` was already bounded (MaxLength 64), but `PassWindow.satellite`
  copied the full external name into up to `MaxPassWindows` (500) windows, the pass-prediction
  TLE-conversion error embedded it unbounded (→ `PassesPredicted` condition), and the deep-space
  rejection summary named satellites unbounded (→ `UnsupportedOrbitRegime` condition). A single long name
  (GP responses allow up to 50 MB) could thus inflate a bounded input into a status object exceeding
  etcd's ~1.5 MB limit — the status write then fails and the reconcile stalls — or pressure controller
  memory. Because the source URL is CR-controlled, the SSRF guard does not imply trusted response content
  (a legitimate public HTTPS endpoint can return a malicious OMM). A shared, rune-safe
  `ephemeris.BoundedSatelliteLabel` now bounds the name to 64 code points at every persist/surface site
  (pass windows, propagated states, and error/condition messages); `PassWindow.satellite` gains a CRD
  `MaxLength=64` as a defense-in-depth apiserver gate; and the canonical satellite identity is the NORAD ID
  (the name is a display label only). The rune-safe truncation also fixes a latent byte truncation that
  could split a multibyte rune into a U+FFFD replacement char. Mutation-tested with a 1 MB `OBJECT_NAME`.
- **A `wss://`→plaintext `http://` handshake redirect can no longer leak the shared secret.** A
  gNB/proxy `302` from `wss://` to a plaintext `http://` on the same host could
  otherwise make the client re-send the `Authorization: Bearer` shared secret over
  cleartext (`coder/websocket` follows redirects by default, and Go preserves the
  header on a same-host redirect). The handshake now refuses redirects
  (`CheckRedirect` → `ErrUseLastResponse`), mutation-tested against the leak.
- **`remoteControl.tls` Secret handling hardened (partial confused-deputy mitigation).**
  The operator reads the referenced Secret with its own cluster-wide `secrets get`, on
  behalf of whoever authored the `NTNCellConfig`; a principal who can write the CR (but
  not read Secrets) could otherwise point the operator at an arbitrary Secret in the same namespace
  and have its `token` shipped to a CR-controlled endpoint. Two gates raise the bar: the
  Secret's **owner** must label it `ntn.operators.dev/remote-control-credential: "true"`,
  and a `kubernetes.io/service-account-token` / `bootstrap.kubernetes.io/token` Secret is
  refused outright. ⚠ **This is attack-surface reduction, not an authorization boundary:**
  the label is namespace-scoped (any `NTNCellConfig` in the namespace may use a labelled
  Secret), a `patch`-only principal could add the label to a Secret it cannot read, and
  the type check does not stop an `Opaque` Secret from holding some other bearer token. A
  real per-CR/endpoint authorization control (SubjectAccessReview or a grant resource) is
  a tracked follow-up.
- **Refused handshake redirects and other definitive HTTP handshake responses (3xx/4xx)
  are classified as permanent**, not transient — so the runtime push does not tight-requeue
  a redirect/auth rejection every minute.
- **Credential-resolution failures no longer leak Secret existence/type to the CR.** The
  `EphemerisPushed=False` condition/event now carries a uniform "credential unavailable or
  not authorized" message; the specific cause is logged for the operator only (closes a
  Secret existence/type oracle for a CR-writer without `secrets get`).
- **SSRF-safe HTTP client hardened against proxy bypass and TLS downgrade.** `NewSafeHTTPClient`
  (used for GP ephemeris fetch, the NTNSlice metrics reader, and the ground-station probe —
  all of which dial CR-influenced hosts) now sets `Transport.Proxy = nil`, so an
  `HTTP_PROXY`/`HTTPS_PROXY` in the environment can no longer make net/http dial the proxy and
  carry the real target past the dial-time IP validation (an SSRF-guard bypass). Its
  `CheckRedirect` also refuses an `https`→`http` downgrade redirect, so a source reached over
  TLS (e.g. Space-Track, whose session cookie would leak) cannot be redirected to cleartext.
  A `nil` client passed to `NewCelesTrakFetcher` now defaults to this safe client rather than a
  bare `http.Client`, so the nil-client fallback no longer yields an unguarded fetcher (a caller
  that passes its own bare `http.Client` still gets exactly what it passed). Mutation-tested.
- **Space-Track credential-Secret failures no longer leak Secret existence/key shape to the CR.**
  A missing Secret, missing password key, or missing username key now all surface the same
  uniform "credentials unavailable or not authorized" message on the CR; the specific cause is
  logged for the operator only. This closes a Secret existence/key oracle for a principal who
  can write the `SatelliteEphemeris` but not read Secrets (mirrors the `remoteControl.tls`
  hardening). A source URL rejected off the trusted origin is also now classified as a permanent
  config error (`InvalidSourceURL`, slow requeue) rather than looping the transient backoff.
- **The authenticated Space-Track response body is never reflected into a CR Condition/Event.**
  The Space-Track fetch is authenticated with the credential Secret and its query URL is
  CR-controlled, so surfacing the response body into a public CR object was an information-flow
  risk; the rate-limit paths now emit a fixed classified message. The query URL is pinned to the
  same origin (scheme + host) as the operator-trusted Space-Track base, and — importantly — that
  exact-origin check is now enforced on **every redirect hop** (a dedicated `CheckRedirect`), not
  only the initial URL: a 307/308 re-POSTs the login credential body and the session cookie
  rides redirects, so a cross-origin redirect that would carry credentials/session off the
  trusted host is refused. The cookie jar also uses the public-suffix list so a server cannot set
  an overly broad domain cookie. Mutation-tested.

### Changed

- **Pass-prediction failures now carry a specific `PassesPredicted=False` reason instead of a blanket
  `PredictionFailed`.** The failure event gate keys on `(Status, Reason, ObservedGeneration)` and
  deliberately ignores the message, so two different root causes at the same generation (e.g. a
  referenced ground station that is absent, then later created with an invalid latitude) produced no
  fresh Event — the `.message` updated, but event history and event-driven alerting could not tell the
  causes apart. Distinct causes are now tagged with stable, low-cardinality reasons —
  `GroundStationNotFound`, `InvalidGroundStationLocation`, `InvalidPredictionConfig`,
  `PredictionComputationFailed` — while a transient/unclassified read still falls back to
  `PredictionFailed`. The message is still ignored by the gate (no Event flood on message-only
  changes), and the `PassesPredicted` *status* is unchanged, so the NTNSlice consumer's 3-state gate
  (which keys on `True`, not the reason) is unaffected. Operators that alerted specifically on
  `reason == PredictionFailed` for these cases should widen to the `PassesPredicted=False` status.

- **Pass prediction now runs on its own lower cadence, off the propagation heartbeat (#234, ADR 0006).**
  The SatelliteEphemeris reconcile propagates and publishes the runtime-push epoch **first**, then runs
  the expensive pass-window sweep only once per 15 minutes (was on every ~3-minute heartbeat) in a
  separate status write. This keeps the sweep's `O(horizon x satellites x ground stations)` cost out of
  the epoch's refresh cadence, so a large fleet can no longer stretch the delivered epoch past its
  validity, and the epoch heartbeat stays fresh even when the sweep is slow or cancelled. Adds an
  additive, controller-owned `status.lastPassPredictionTime` field. The sweep also re-runs
  IMMEDIATELY when a pass-prediction input changes (ground stations added/edited/removed, NORAD
  selector, minElevation, horizon, or source) instead of waiting out the interval, so it never leaves
  stale windows for a downstream consumer; this is tracked by a new `status.lastPassPredictionInputHash`
  field. Mutation-tested.

- **NTNSlice failover now honors the pass-prediction validity contract (#234, ADR 0006).** The
  consumer (`NTNSlice.checkSatelliteAvailability`) no longer reads `NextPassWindows` directly; it
  treats satellite-pass availability as KNOWN only while the producer's `PassesPredicted` condition is
  `True`. `Unknown` (recomputing after an input change or a no-OMM failure), `False`
  (`PredictionFailed`), and an absent condition all mean UNKNOWN, so the slice holds its current path
  instead of misreading stale or empty windows as a real end-of-pass (a transient prediction failure no
  longer drops a slice off a satellite that is actually overhead). On an input change the producer clears
  the stale windows and marks `PassesPredicted=Unknown` in the epoch (first) write, so a consumer reading
  between the two writes holds; `handleSetupFailure` and `handleInsecureURL` join `handleFetchError` in
  clearing pass status on any no-OMM path; a terminating ground station is excluded from the sweep; and a
  future-dated cadence timestamp self-heals. A new `-race` CI job covers the two-write path.
  Mutation-tested.

- **Runtime-push currency/freshness lifecycle now has manager-backed envtest coverage.**
  The regression drives the real apiserver status subresource and informer cache
  through same-epoch deduplication, a propagation-input generation bump that fails
  closed while status is stale, and exactly one re-push after status catches up
  without a hot requeue (#252).
- **The runtime-push epoch-only freshness re-push now has manager-backed envtest coverage.**
  An added step to that spec holds the ephemeris spec, generation, and propagation input hash
  fixed while advancing only the propagated epoch, then requires exactly one re-push carrying the
  new epoch and a persisted marker asserted **independently** of `runtimeEphemerisPushMarker` — so a
  regression that drops `epoch` from the dedup key is caught rather than masked. Closes the
  mutation gap the #252 coverage left open (#255). A `minAvailable: 1` PDB over
  a single replica blocks every eviction-API disruption (node drain, descheduler, autoscaler) — the
  one pod can never be evicted (it does not gate Deployment rolling updates, which delete pods
  directly). The chart defaults to 2 replicas, but a `replicas: 1` override previously still emitted
  the PDB and would deadlock drains; it is now gated on `replicas > 1`, so a single-replica install
  stays drainable (the `config/manager` kustomize base ships 1 replica and no PDB, so it was never
  affected) (#238, part of #232).
- **`passPrediction.horizon` is bounded and pass prediction is cancellable.** Pass prediction
  sweeps the whole horizon for every satellite × ground station, so an unbounded `horizon` could
  stall the reconcile (and, transitively, delay the runtime-push epoch). The horizon is now
  clamped to a 7-day maximum at reconcile time; the clamp bounds the horizon dimension of one work
  item's cost (total reconcile cost still scales with the selected satellites × ground stations,
  whose stored output `MaxPassWindows` caps). A NEGATIVE horizon is now rejected as a config error
  rather than silently defaulting to 24h. `PredictPasses` also takes a `context.Context` and honours
  cancellation BETWEEN work items: a cancelled or timed-out call stops dispatching new items and
  returns `ctx.Err()`, though an item already running finishes because the upstream
  `sgp4.GeneratePasses` sweep takes no context — at the 7-day clamp a single item is ~20ms
  (`BenchmarkPredictPasses_7d_*`), so a cancelled call returns within roughly that bound. The
  reconcile treats such a cancellation as control flow (requeue), not a `PredictionFailed` error.
  ⚠ `ephemeris.PredictPasses` now takes `ctx` as its first argument (source-breaking for any
  external Go caller) (#233, part of #232).
- **`/readyz` now gates on informer cache-sync (still leadership-agnostic).** It was
  `healthz.Ping`, which reports Ready as soon as the process serves — but controller-runtime
  starts the health server *before* the caches sync, so a broken new replica (RBAC, CRD
  discovery, a wedged list/watch) could be counted Available and a rollout would then tear down
  the healthy old replicas. `/readyz` now passes only once the caches have synced, wired via a
  non-leader-election `Runnable` (`NeedLeaderElection()==false`) so every replica — leader or
  standby — becomes Ready after its *own* cache sync, without gating on leadership (which would
  deadlock rollouts: a standby could never become Ready). `/healthz` stays `healthz.Ping`.
- **`passPrediction.minElevation` is constrained to `[0, 90]` degrees.** 0 is the
  geometric horizon and 90 the zenith; a negative or `>90` mask is physically meaningless.
  Enforced by an admission CEL rule plus a finite-value runtime range check. The value
  pattern otherwise preserves the prior grammar (a trailing dot such as `"10."` stays
  valid — only the leading `-` was removed). The bound is on the parsed **float64** value
  (the pass pipeline is float64), so a literal within ~half a ULP above 90 rounds to 90 and
  is accepted; `NaN`/`Inf` are rejected (#201-P3).
- **NTNSlice anti-flap minimum-dwell survives a controller restart or leader-election handoff.**
  The post-switchback min-terrestrial-dwell clock (`LastSwitchback`) was in-memory only, so a
  handoff reset it to "dwell satisfied" and a re-degradation within the dwell window could fail
  over earlier than intended. It is now mirrored to a new `NTNSlice.status.lastSwitchbackTime`
  and **monotonically merged** with the in-memory clock: status is adopted when it is newer (a
  cold cache after restart), and repaired when it is behind. The in-memory clock is committed only
  **after** the durable status write succeeds (the same "commit side-effects once status is
  durable" discipline as the failover counter/event), so a switchback whose write fails cannot
  leave a speculative timestamp that a later pass-ended forced switchback could launder into a
  bogus dwell. A future-dated status value (a backward node-clock step or an external edit; the
  controller only ever writes values `<= now`) is **cleared durably** when the reload observes it,
  not merely ignored, so it cannot silently become valid history once the wall clock passes it and
  block a legitimate failover with a ghost dwell. The in-memory anti-flap cache is also keyed by
  object **UID** as well as name, so a same-name NTNSlice recreated with a new UID (a delete+create
  the workqueue coalesced into one reconcile, hiding the intervening NotFound) does not inherit the
  deleted object's counters or dwell clock. The other two
  flap clocks stay in-memory (their loss only delays a switch, never advances one). This matters
  more now that rolling updates hand leadership over routinely. **Scope:** durability holds once
  this version has recorded at least one quality-driven switchback; a switchback recorded only by
  an older (pre-field) version is not recoverable, and min-dwell accuracy across a handoff assumes
  the controller nodes have synchronised clocks (#220 H1).

### Fixed

- **SGP4 propagation failures are now surfaced, not silently dropped.** A tracked element set that
  passed the epoch checks but failed `PropagateToECEF` (OMM→TLE conversion, propagation, or ECEF range
  validation) was dropped from `propagatedStates` with a bare `continue` — no condition, metric, or
  count — so a malformed/non-propagatable element set looked like a generic downstream
  `EphemerisPayloadMissing`. The producer now counts these and surfaces them via a `PropagationFailed`
  status condition and the `ntn_operators_ephemeris_propagation_failed_count` gauge (cleaned up on CR
  deletion). The condition message carries only a bounded reason and up to three example NORAD IDs —
  never raw external fields. Mutation-tested.

- **NTNSlice no longer fails over to a satellite whose ephemeris is too stale to deliver.** NTNSlice
  keyed satellite availability on the producer's `PassesPredicted` condition plus an active pass window,
  while NTNCellConfig gates the runtime push on the element set's source-epoch freshness
  (`sourceEpochFresh` / `maxEpochAge`). During a `>maxEpochAge` upstream outage the two consumers of
  `PropagatedStates` disagreed: pass prediction kept succeeding off the stale, drifting OMM so NTNSlice
  reported the satellite available and steered traffic onto it, but NTNCellConfig then refused to push
  its stale ephemeris to the gNB — a control-plane split that stranded traffic on an unconfigurable
  path. `checkSatelliteAvailability` now demotes an active window whose satellite has a present-but-stale
  propagated state (correlated by NORAD — `PassWindow` gains a `noradID` field), so both consumers share
  one freshness contract; the `FailoverReady=SatelliteUnavailable` message cites the staleness.
  Mutation-tested, including an end-to-end reconcile proving a fired terrestrial trigger does not fail
  over to a stale satellite.

- **Runtime satellite selection is now deterministic and fails closed instead of silently switching
  satellites.** `SatelliteEphemeris` propagated states inherited the upstream response order
  (`FilterOMMs` preserves it; no dedup, no sort), so with a multi-satellite ephemeris and no
  `NTNCellConfig.spec.ephemerisNoradID` the cell pushed "the first" state — and a reordered upstream
  response (`[A,B]`→`[B,A]`, which neither CelesTrak nor Space-Track guarantees stable) silently
  switched which satellite was pushed, while a duplicate NORAD (e.g. a `GP_HISTORY` query) could serve
  an older element set. The producer now keeps, per NORAD, the latest-epoch element set and sorts by
  NORAD ascending; the runtime-push consumer fails **closed** (`EphemerisPushed=False`, reason
  `EphemerisSelectionAmbiguous`) when `ephemerisNoradID` is unset against more than one satellite,
  rather than guess. A single-satellite ephemeris still resolves implicitly. Mutation-tested.

- **Runtime-push dedup key is now a content digest, not a wall-clock epoch treated as monotonic.**
  `runtimeEphemerisPushMarker` keyed on the propagated `EpochUnixMs` alone, whose comment claimed it
  was a monotonic 1:1 proxy for the ECEF. But `EpochUnixMs` is serialized wall-clock time with no
  monotonic reading, so across pods or an NTP step a repeated target epoch carrying a *different* ECEF
  (a new OMM propagated to the same instant after a clock rewind/skew, with the ephemeris generation
  unchanged) produced an identical marker and the fresh position/velocity was silently dedup'd. The
  marker now digests the delivered content — owner UID, propagation-input hash, NORAD, both epochs, and
  the full ECEF position+velocity — so it changes iff the payload changes (a fresh epoch is one such
  change, preserving the per-propagation re-push cadence). Mutation-tested for the same-epoch/changed-ECEF
  case the epoch-advances test (#260) did not cover.

- **The manager waits indefinitely for runnables on shutdown, so the leader lease is never released
  early.** `GracefulShutdownTimeout` was unset (controller-runtime default 30s). A *finite* timeout is
  unsafe with `LeaderElectionReleaseOnCancel`: `runnableGroup.StopAndWait` returns on ctx-expiry
  *without* the runnables draining, and the deferred lease release then hands the lease to a standby
  while a hung reconcile is still doing lease-guarded work — a split-brain window (controller-runtime
  #1132). `GracefulShutdownTimeout` is now negative (wait indefinitely): the lease is released only
  after the runnables truly stop; a hung pod is SIGKILLed at `terminationGracePeriodSeconds` *without*
  releasing, degrading safely to a `LeaseDuration` failover. `terminationGracePeriodSeconds` is raised
  to 30s across all three deployment manifests (kustomize, Helm, OLM bundle) so it stays ≥ the
  lease-release `RenewDeadline` — release *headroom* when runnables stop promptly, not a guarantee (a
  too-short remainder falls back safely to lease-expiry failover). Unit tests pin the negative-timeout
  wiring reaching the manager options and the grace-period floor against the parsed kustomize and
  OLM-bundle PodSpecs (#238, part of #232).
- **The propagated ephemeris epoch is stamped at propagation time, not reconcile-start.** The
  epoch was derived from the clock sampled at the top of the reconcile, but the fetch and pass
  prediction run before propagation, so the delivered epoch was already aged by that compute —
  eating into the `propagationEpochLead`. The clock is now re-sampled immediately before
  propagation, so the epoch reflects the true propagation instant (the fetch/pass-prediction paths
  keep the reconcile-start `now`, which is correct for their reconcile-relative windows) (#235,
  part of #232).
- **GP-fetch retry ramp restarts on an episode change instead of leaking its count across error
  classes.** The consecutive-failure counter is shared, but only the transient backoff branch
  consumes it — a run of auth (or rate-limit) failures used to leave the count high and slam the
  first following *transient* failure straight past the 1-minute ramp base (up to the whole refresh
  interval), stranding fetch recovery long after the blip cleared. The ramp now restarts whenever
  the retry episode changes — a new error class, or a new `retryKey` (a spec/interval edit) — while
  a genuine same-episode transient run still ramps 1m, 2m, 4m … (#236, part of #232).
- **Element-set epoch health is counted across the whole tracked set, not just the pre-cap subset.**
  Unparseable / implausibly-future OMM EPOCHs were counted only inside the `maxPropagatedStates`
  (128) propagation loop, yet the `SourceEpochRejected` condition reports them against the full
  tracked count — so a larger-than-cap constellation under-reported rejections sitting beyond the
  cap. The counting now runs in a full-set pre-pass, keeping numerator and denominator consistent
  (#236, part of #232).
- **Pass windows are computed from the current time, not the last fetch time.** On a cached
  re-propagation the fetch timestamp could be up to 24h in the past, which started AOS/LOS
  windows stale; they now start at reconcile time (#200-C3).
- **CelesTrak rate-limit errors surface the upstream 403 reason.** CelesTrak (unauthenticated)
  returns an explanatory 403 body (e.g. "GP data has not updated since your last successful
  download"); it is captured into the error/condition, sanitized to a single line and bounded
  by ENCODED bytes (invalid UTF-8 and format/bidi/zero-width chars are stripped, not
  re-encoded) (#200-C6). **The authenticated Space-Track path never reflects its response body**
  — it uses a fixed "Space-Track query rate limit exceeded" message (see Security).
- **`Retry-After` delay-seconds of any length saturate to the 24h cap.** A value exceeding
  `int`/`int64` range is still a valid RFC 9110 integer; it is now parsed as an unsigned value
  and saturated to the cap rather than falling through to the date branch and returning 0
  (#200-C7).
- **SIB19 now survives a sustained upstream outage, not just the first reconcile of one.**
  Serving cached OMMs on a failed GP fetch (I-18) used to re-propagate ONCE (to
  `now + 5 min`) and then set `RequeueAfter` to the **fetch** backoff — 2–24h for
  rate-limit/auth failures, or the workqueue's exponential backoff for a transient one. The
  controller filters its own status writes, so nothing else re-triggered it: the pushed epoch
  expired ~5 minutes in and the consumer refused the state for the rest of the window
  (continuity was ~4%, not 100%). The two cadences are now independent — the **fetch** is
  throttled on the cache entry (`nextFetchAttempt`) while the **reconcile** keeps running on
  the 3-minute propagation heartbeat, so the source is contacted once per backoff window and
  the epoch is refreshed for the whole outage. A **fetcher/credential setup failure** (missing,
  unreadable or mid-rotation Secret) now takes the same cache fallback instead of giving up
  without propagating, reported distinctly as `FetcherSetupFailedServingCache`. Fixing the
  credentials or lowering `refreshInterval` clears a stale backoff immediately, and an auth
  backoff is capped so an in-place Secret fix recovers in minutes rather than up to 24h.
  ⚠ **Scope:** the OMM cache is process-local and in-memory, so this preserves continuity for
  the lifetime of a **warm** controller cache. After a restart or leader failover the cache is
  cold, and if the upstream is still unavailable there is nothing to re-propagate from.
- **`propagatedStates[].sourceEpochUnixMs == 0` no longer bypasses the freshness gate.** `0`
  meant *both* "unparseable" *and* the legal instant `1970-01-01T00:00:00Z`, and the consumer
  failed **open** on it — so a 1970-dated element set skipped the 7-day hard-stale check
  entirely. The producer no longer emits a state whose epoch it could not parse (SGP4's own
  `OMM.ToTLE` parses the same epoch, so such an element set fails propagation anyway), and the
  consumer now validates unconditionally: a 1970 epoch is simply, and correctly, stale. Element
  sets refused before propagation are surfaced by a new `SourceEpochRejected` condition
  (`UnparseableSourceEpoch` / `FutureDatedSourceEpoch`), so a cell reporting
  `EphemerisPayloadMissing` can be traced to a corrupt feed rather than a NORAD typo.
- **Runtime ephemeris push is gated on propagation-input currency and per-satellite
  freshness.** The `NTNCellConfig` runtime push consumes `SatelliteEphemeris.status.propagatedStates`
  across a watch fan-out; it now refuses to push when those states were computed under
  different propagation inputs than the live spec (`status.propagatedStatesInputHash` — a
  digest of source type/url + NORAD selector, NOT the whole spec) so a source/selector edit
  whose re-propagate has not yet succeeded can't ship stale orbital data (reason
  `EphemerisInputsStale`, non-requeuing). Freshness is now **per-satellite** (new
  `propagatedStates[].sourceEpochUnixMs`): a stale sibling in the same `SatelliteEphemeris`
  no longer blocks a cell whose selected satellite is fresh.
- **The runtime push re-validates currency on EVERY reconcile, not just when a new push is
  imminent.** The dedup ("already pushed this marker") check now runs AFTER the epoch and
  source-epoch gates. Previously a stalled producer — controller down, fetcher-setup
  failure, upstream outage — stopped re-propagating, so the dedup marker stopped changing
  and every reconcile short-circuited past the freshness gates while the already-delivered
  epoch expired on the gNB, all while `ephemeris_push_ready` stayed at `1` (falsely
  healthy). The push now fails closed (`EphemerisPushed=False`, `ephemeris_push_ready=0`)
  once the delivered data goes stale, so `push_ready == 0 for 15m` alerts (issue #216).
- **The in-memory OMM cache is keyed on the upstream payload identity (source type + URL),
  not `.metadata.generation`.** Generation bumps on ANY spec edit — including
  `passPrediction` / `refreshInterval`, which do not change the upstream payload — and
  dropping the cache on those edits left a fetch that failed right afterwards with NO
  fallback, so the producer could not re-propagate and SIB19 continuity broke. A
  non-propagation edit now keeps last-good OMMs usable, so re-propagation survives an
  upstream outage. (The NORAD selector is excluded from the cache key on purpose: it is
  applied client-side, so a selector edit must not discard the raw OMMs.)
- **An implausibly future-dated element set is rejected by the producer, before SGP4 runs**
  (not only at the push). A far-future epoch from a corrupt or spoofed feed would otherwise
  drive SGP4 *backward* from the bogus epoch and write a wildly wrong ECEF into status; the
  consumer check alone could only block its delivery, not the propagation.
- **The gNB ConfigMap is no longer rewritten byte-identically on every reconcile.**
  `ApplyCellConfig` produces a deterministic, static-spec-derived config, but it Updated the
  ConfigMap unconditionally — so each `SatelliteEphemeris` re-propagation (~3 min) fanned out
  to every referencing `NTNCellConfig` and bumped the ConfigMap's `resourceVersion`, churning
  every watcher. It now skips the write when the stored content (config + koffset annotation)
  already matches and the ConfigMap is already owned (#204-G3).
- **`NTNCellConfig` failure early returns no longer re-write an identical status every requeue.**
  The reconcile already skipped a byte-identical *terminal* status write, but the failure early
  returns — provider registry/type unusable, `ApplyCellConfig` failing, post-apply `GetCellStatus`
  failing, and an ephemeris-push failure — still called `Status().Update` unconditionally, so a
  persistent outage re-sent an identical `ConfigApplied=False`/`EphemerisPushed=False` through API
  handling, admission, and the watch stream on every (minutely) requeue. A `persistStatusIfChanged`
  helper (mirroring the NTNSlice one) now guards every status write, terminal and early-return alike;
  the episode-gated events keep the WO-20 emit-after-persist discipline (any change that would emit
  also fails the DeepEqual and writes). Verified by counting status subresource requests, since the
  apiserver short-circuits a byte-identical write without a `resourceVersion` bump.
- **`SatelliteEphemeris`→`NTNCellConfig` fan-out uses a `spec.ephemerisRef` field index.**
  The mapper resolved referencing cells by scanning every `NTNCellConfig` in the namespace and
  filtering in Go; it now uses an indexed cache lookup (registered in `SetupWithManager`), with
  a fallback to the scan for non-cache clients (#204-G3).
- **Pass windows (`status.nextPassWindows`) are now conservative to the whole second.**
  AOS is rounded up and LOS down before storage, so the persisted window stays within the
  window the mask-trim computed — previously AOS/LOS carried ~200 ms of sub-second residual
  and `metav1.Time` serializes at second granularity without the fractional second, which
  pushed the stored AOS *earlier*, over-claiming availability by up to a second (#201-P2-1).
  (When mask-boundary evaluation succeeds that computed window is the usable ≥ minElevation
  interval; on the fail-open elevation-error path it is the wider 0°-horizon window — see
  the deferred narrow fail-closed proposal.)
- **Chart values docs corrected + HA design documented.** `dist/chart/README.md` listed
  `podDisruptionBudget.enable` as `false` and `manager.affinity` as `{}`, but the `values.yaml`
  defaults are `true` and a soft `podAntiAffinity`. Added `docs/high-availability.md` covering
  the active-passive design (leader election, `LeaderElectionReleaseOnCancel`, PDB, anti-affinity,
  cache-sync readiness, failover behavior). The Helm chart is the HA path — process-level
  active-passive with **best-effort** node spreading (soft anti-affinity + PDB guard voluntary
  disruptions only; not a node-failure guarantee); the `config/default` kustomize base is
  intentionally single-replica for dev/e2e. Corrected an earlier claim that a cache-sync-gated
  readyz would deadlock rollouts — only a *leadership*-gated one does.
- **Docs/runbook corrections (audit follow-up).** `RELEASING.md`'s operator-only
  rollback now uses `helm upgrade oci://ghcr.io/thc1006/ntn-operators --version
  <prev> --reuse-values --set crd.enable=false`; the previous
  `helm rollback … --set crd.enable=false` fails with `unknown flag: --set`
  (`helm rollback` takes no `--set`), and a local `dist/chart` path ignores
  `--version` (would redeploy current sources, not roll back). In the NOC runbook
  (`docs/runbooks/alerts.md`): the gNB-reachability check now uses an ephemeral
  debug container in the operator Pod (shares its netns → governed by the
  operator's NetworkPolicy; a bare labelled `kubectl run` pod would be adopted and
  killed by the operator ReplicaSet) with IPv6-safe endpoint parsing; the "all
  alerts are namespaced" claim is corrected (`NTNControllerReconcileErrors` is
  labelled by `controller` only); log commands use a release-independent label
  selector with `--tail=-1 --prefix=true` (the selector default is 10 lines); and
  the `GPDataFetched=False` reason list is completed
  (`FetcherSetupFailed`/`InsecureURL`). The chart's
  `extraEgress` help no longer implies it can *restrict* egress — NetworkPolicy
  egress rules are additive, so a port must be left out of
  `prometheusPorts`/`gnbPorts` to be CIDR-scoped. The README no longer states
  stale-CRD writes are unconditionally "silently dropped" (strict server-side
  field validation rejects them). The `runtimeEphemerisPushMarker` comment no
  longer claims "NR max 240 s" (the `ntnUlSyncValidityDur` enum caps at 900 s)
  or that the re-push cadence is always under `ntn-UlSyncValidityDur`.
- **The `failoverPolicy.triggers` admission rule rejects non-finite / overflowing values.** The
  value regex now bounds the digit counts (≤10 integer, ≤10 fraction, optional ≤2-digit
  exponent), so a form like `latency > 1e9999` — which parses to `+Inf` — is rejected at
  admission instead of only at runtime (`ParseTrigger` already fail-closes `NaN`/`Inf`). The
  admission rule now truly enforces the "finite number" its message promises and stays in
  lockstep with the runtime parse (the `trigger_cel_test` agreement corpus now covers `1e9999`)
  (#220 C4).

### Upgrade notes

- **A satellite whose element set is stale beyond `maxEpochAge` now holds NTNSlice on terrestrial**
  instead of failing over to it — previously NTNSlice could steer traffic onto a satellite whose stale
  ephemeris NTNCellConfig refused to push. No action needed; refresh the `SatelliteEphemeris` source to
  restore satellite availability. `PassWindow` gains an optional `noradID` field (additive, backward
  compatible; existing windows repopulate it on the next prediction).
- **Runtime push now fails closed for a multi-satellite ephemeris with no `ephemerisNoradID`.** An
  existing `NTNCellConfig` on the runtime-push path that left `spec.ephemerisNoradID` unset while its
  referenced `SatelliteEphemeris` tracks more than one satellite will now hold with
  `EphemerisPushed=False` (reason `EphemerisSelectionAmbiguous`) instead of pushing whichever
  satellite the upstream listed first. Set `spec.ephemerisNoradID` to the intended satellite to
  restore pushing. Single-satellite ephemerides are unaffected.
- **One-time runtime re-push per cell after upgrade (harmless).** The runtime-push dedup marker format
  changed (epoch → content digest), so an already-pushed `NTNCellConfig`'s stored marker will not match
  the new one on the first reconcile, triggering a single re-push of the current (correct) propagated
  state. It is idempotent and self-healing; no action needed.
- **`passPrediction.horizon`: negative is rejected, over-7-days is clamped.** A horizon > 7 days is
  clamped to 7 days (surfaced on the `PassesPredicted` condition message, not silently), so a spec
  relying on windows beyond 7 days will see them truncated (`MaxPassWindows` already capped the
  output at 500). A NEGATIVE horizon is now rejected with a config error instead of silently
  defaulting to 24 hours. `ephemeris.PredictPasses` also gains a leading `context.Context` argument
  (source-breaking for external Go callers).
- **`NewSafeHTTPClient` no longer honors `HTTP_PROXY`/`HTTPS_PROXY`.** The GP fetch, NTNSlice
  metrics reader, and ground-station probe reach in-cluster or public endpoints directly; a
  deployment that previously relied on an egress proxy for those must expose the endpoints
  without a proxy (the proxy fundamentally bypasses the SSRF dial guard, so it is disabled by
  design). An opt-in proxy that re-validates the CONNECT target is a possible future enhancement.

- **Breaking (`remoteControl.tls`):** an existing remote-control credential Secret must
  gain the label `ntn.operators.dev/remote-control-credential: "true"` (set by the
  Secret owner), or the runtime push reports `EphemerisPushed=False` with reason
  `RemoteControlCredentialUnavailable`. Because the referenced Secret is not watched, the push
  retries on a bounded ~5-minute self-heal poll (not a tight per-minute loop), so after
  you add the label the cell recovers within ~5 minutes on its own — no spec edit or
  operator restart needed. Add the label before upgrading. Note the opt-in is
  namespace-scoped; a per-CR grant is a future hardening.
- **Runtime push is fail-closed for one reconcile cycle after upgrade.** Existing
  `SatelliteEphemeris` objects have no `status.propagatedStatesInputHash` until the
  controller re-propagates them, so the runtime push briefly reports
  `EphemerisPushed=False` (reason `EphemerisInputsStale`) until the first successful
  post-upgrade fetch+propagate stamps the hash. The gNB retains its last-pushed config in
  the meantime. Ensure the GP source (CelesTrak/Space-Track) is reachable during the
  rollout — a fetch outage coinciding with the upgrade (e.g. a CelesTrak per-update-window
  rate-limit) extends the window until the next successful fetch. This is intentional
  fail-closed behavior (never push data of unverified currency), not a data loss.
- **`passPrediction.minElevation` must be within `[0, 90]`.** New objects, and any edit that
  CHANGES `minElevation` itself, must be in range. Because of Kubernetes CRD validation
  ratcheting (default from 1.30; the project's min is 1.31), an existing object with an
  out-of-range value is NOT necessarily rejected on an unrelated edit — the illegal value can
  persist in etcd until `minElevation` is next changed. In that state the controller's runtime
  check refuses to use the value, clears `status.nextPassWindows`, and reports
  `PassesPredicted=False`/`PredictionFailed`. Correct any out-of-range `minElevation` before
  upgrading (the default "10" is unaffected).
- **`failoverPolicy.triggers` validation is tightened to bounded-magnitude numbers.** The trigger
  value now admits at most 10 integer + 10 fraction digits and an optional 2-digit exponent, so
  overflowing forms like `1e9999` are rejected at admission. Realistic RSRP/latency/packet-loss
  thresholds are unaffected. The CEL rule is scoped to the whole `failoverPolicy` object, so with
  CRD ratcheting (min K8s 1.31) an existing NTNSlice carrying such an exotic value is re-validated
  (and rejected) as soon as **any** part of `failoverPolicy` is edited — including
  `switchbackDelay` — while edits outside `failoverPolicy` are ratcheted through. The runtime
  already fail-closes the value regardless. Correct any such trigger before editing
  `failoverPolicy`.
- **`NTNSlice.status.lastSwitchbackTime` may be dropped on a downgrade.** A controller version
  that predates this field does a full `/status` PUT with a Go struct that does not know the
  field, so rolling the controller back can remove it. min-dwell durability is therefore not
  guaranteed across a downgrade — the next quality-driven switchback on the (re-upgraded) new
  version re-establishes it. Forward upgrades are unaffected.

## [0.7.0] - 2026-07-12

The audit season: runtime-resilience, validation, event/GitOps, and observability
hardening across all four controllers (folds the #198–#215 remediation work).
Backward-incompatible in two ways — read the Upgrade notes before upgrading.

### Upgrade notes

- **Breaking: minimum Kubernetes version is now 1.31.** The
  `provider.remoteControl.endpoint` CEL validation uses the `isIP()` library, which
  is available only from Kubernetes 1.31; `Chart.yaml` `kubeVersion` is `>=1.31.0-0`
  and installing on an older cluster is unsupported.
- **Breaking: tighter spec validation may reject previously-accepted objects.** Every
  free-form string now carries a `maxLength` and every unbounded list a `maxItems`, so
  an object authored under ≤ 0.6.0 that exceeds a new bound is rejected on its next
  apply/edit (unset fields are unaffected). Review long free-text and large-list fields
  before upgrading.
- **CRDs upgrade separately on Option A (`--set crd.enable=false`).** Run `make install`
  at the new tag **before** `helm upgrade`, or the new operator runs against the old
  CRD schema. See [RELEASING.md](RELEASING.md) § Rollback.

### Added

- **`RefreshIntervalClamped` condition on `SatelliteEphemeris`.** Surfaces whether
  `spec.source.refreshInterval` was clamped into the supported `[2h, 24h]` range:
  `True` (reason `BelowMinimum`/`AboveMaximum`) when clamped, `False`
  (`WithinBounds`) when the configured interval is used as-is. The controller always
  sets it explicitly, so an absent condition means "not yet reconciled" (Unknown),
  distinct from a `False`.

### Changed

- **Controller event hygiene.** Success and state-transition Events are emitted
  after the reconcile's durable status write, and failure Warnings are episode-gated
  on a condition's status/reason/observedGeneration rather than fired every reconcile,
  so a stuck or churning reconcile no longer floods the Event stream. (One documented
  exception: `RefreshIntervalClamped` is emitted pre-persist because it is a
  deterministic function of the spec, and may therefore duplicate after a status
  conflict.) A controller never writes a CR's `.spec` (only `.status` and finalizers);
  GitOps posture is documented in [`docs/gitops.md`](docs/gitops.md).
- **Validation tightening (may reject previously-accepted values).** Every
  free-form spec string now carries a `maxLength` and every unbounded list a
  `maxItems`, so an over-long or unbounded value is refused at admission instead
  of bloating etcd or a downstream ConfigMap. Caps follow the field's real
  domain: Kubernetes names/namespaces 253/63, URLs 2048, free text 1024,
  numeric-string fields 32, `satellites.noradIDs` 512 items, `bands`/`groundStations`
  lists bounded with per-item length. Objects authored under ≤ 0.6.0 that exceed
  a new bound are rejected on their next apply/edit (unset fields are unaffected).
- **`provider.remoteControl.endpoint` validation hardened.** Beyond the existing
  `host:port` shape check, CEL now enforces the port range (1–65535), that a
  bracketed host is a valid IP (`[::1]:8001`), that an all-numeric host is a real
  IPv4 (so `999.999.999.999:1` is refused rather than treated as a hostname), and
  the RFC 1035 DNS length limits (whole host ≤ 253, each label 1–63). A mistyped
  endpoint is now a permanent admission error instead of a silent tight-requeue.
- **Minimum Kubernetes version raised to 1.31.** The endpoint host CEL rules use
  the `isIP()` / IP-address CEL library, which is available only from Kubernetes
  1.31. `Chart.yaml` `kubeVersion`, the OLM CSV `minKubeVersion`, and the Nephio
  package/compat docs are updated in lockstep.
- Added condition/status print columns to the CRDs for quicker `kubectl get`
  triage.

### Deprecated

- `SatelliteEphemeris.spec.satellites.constellation` has never been consumed by the
  controller (it performs no filtering) and is now deprecated. Select a CelesTrak
  constellation through `spec.source.url` (the `GROUP=` query parameter, e.g.
  `GROUP=oneweb`), or filter explicit satellites with `spec.satellites.noradIDs`.
  The field remains accepted in `v1alpha1`; removal is deferred to a future
  versioned API migration (conversion + stored-object migration), tracked as a
  separate issue — a version rename alone does not safely drop the data.

### Fixed

- **`ntn_operators_ephemeris_push_errors_total` now counts every push failure**,
  not once per failure episode. The shipped `NTNEphemerisPushFailing` alert is
  `increase(...[15m]) > 0 for 15m`, which only keeps firing while the counter keeps
  advancing; counting once per episode let the alert resolve mid-outage while a
  transient gNB push kept failing (and tight-requeuing) every minute. This matches
  the metric's name/Help and `ntn_operators_config_apply_errors_total`. The
  `EphemerisPushFailed` Kubernetes Event remains episode-gated. (Permanent,
  non-requeuing reasons still increment once; the durable `EphemerisPushed=False`
  condition covers those, with a readiness gauge as a recommended follow-up.)

## [0.6.0] - 2026-07-09

Promotion of `0.6.0-rc.1` after a short soak (the rc's release pipeline —
build, Trivy scan, cosign signing, ghcr image + Helm chart + SBOM — published
clean). No CRD, controller, or config changes since the rc; same tree,
re-tagged as the stable release. The full v0.5.0 → 0.6.0 delta is in the
`[0.6.0-rc.1]` section below.

### Upgrade notes

- **CRDs upgrade separately on Option A (`--set crd.enable=false`).** `make
  install` at the new tag **before** `helm upgrade`, or the new operator runs
  against v0.5.0 CRDs and the runtime NTN push silently no-ops (the apiserver
  drops the fields the old schema lacks). See the README "Upgrading (CRD skew)"
  note. Rollback guidance: [RELEASING.md](RELEASING.md) § Rollback.
- **Breaking: `siWindowPosition` minimum raised 0 → 2.** SIB19 must be scheduled
  after the SIB2 the emitter always adds, so `spec.cellOverrides.sibSchedule.siWindowPosition`
  now validates as `>= 2` (default 2). NTNCellConfigs authored under ≤ v0.5.0
  with an **explicit** `siWindowPosition` of `0` or `1` are rejected by the
  v0.6.0 CRD on their next apply/edit (objects left unset are unaffected — they
  pick up the new default of 2). Migrate before upgrading the CRD:

  ```bash
  # List NTNCellConfigs pinned below the new minimum.
  kubectl get ntncellconfig -A -o json \
    | jq -r '.items[] | select(.spec.cellOverrides.sibSchedule.siWindowPosition != null and .spec.cellOverrides.sibSchedule.siWindowPosition < 2) | "\(.metadata.namespace)/\(.metadata.name)"'
  # Patch each to a valid value (2 = the new default/minimum).
  kubectl patch ntncellconfig <name> -n <ns> --type=merge \
    -p '{"spec":{"cellOverrides":{"sibSchedule":{"siWindowPosition":2}}}}'
  ```

## [0.6.0-rc.1] - 2026-07-09

Config-emitter correctness and runtime NTN push, verified end-to-end against a
real OCUDU gNB (commit `0b229d35`).

### Fixed

- **OCUDU config never parsed on a real gNB.** The generated `ntn:` block sat at
  the top level; OCUDU (`config_extras_mode::error`) rejects unknown top-level
  keys. It is now nested under `cell_cfg.ntn` with the exact keys OCUDU expects
  (`pci`/`carrier_freq`, `feeder_link`, `gateway_location`, `ncells`, …).
- **Systematic value-unit mismatch.** The emitter wrote raw 3GPP codepoints where
  OCUDU expects physical SI. Positions/velocities/TA/angles/eccentricity are now
  converted (e.g. `pos_x = codepoint × 1.3 m`, angles → radians), with CRD ranges
  tightened so a codepoint can never exceed OCUDU's accepted physical range.
- **SIB19-only schedule was rejected by the gNB.** OCUDU requires a SIB with
  ID < 15 alongside SIB19; the emitter now schedules SIB2 (`schedulingInfoList`)
  before SIB19 (`schedulingInfoList2`), so the config boots and broadcasts SIB19.
- **Pass-window failover blindness (#C1):** a global propagation cap could starve
  some satellites of predicted passes; the cap is now per-satellite.
- **Prometheus series leak (#C3):** per-CR metric series are now cleaned up
  (`DeletePartialMatch`) in the finalizers on CR deletion for NTNCellConfig,
  SatelliteEphemeris, and GroundStationLifecycle. (NTNSlice's series are not yet
  covered — see Notes.)
- **Runtime-push stale-epoch tight loop:** a past/expired ephemeris epoch is now
  skipped (not pushed) and permanent gNB rejections no longer tight-requeue.
- **Runtime-push epoch accuracy:** the pushed ephemeris epoch is now stamped
  near-now (not up to a refresh-interval ahead). OCUDU internally re-propagates
  the state vector from its epoch, so a far-future epoch forced a long backward
  propagation and compounded LEO error; a near-now epoch keeps it short/forward.
- An unparseable gNB WebSocket reply is now a retryable failure instead of a
  silent success.

### Added

- **Runtime NTN config push (#176).** SGP4-propagated ECEF now flows to the gNB
  live via OCUDU's `ntn_config_update` remote-control WebSocket (MR !798) instead
  of being discarded: `SatelliteEphemeris.status.propagatedStates`,
  `NTNCellConfig.spec.cellID` + `spec.provider.remoteControl`, and the provider
  `PushRuntimeUpdate` transport.
- **`k_mac` (3GPP `kmac-r17`, #52)** via the runtime `sat_switch_with_resync`
  block — the only OCUDU surface that accepts it. `SatSwitchNTNConfig.kMac`
  (1..512) is delivered by the runtime push.
- **ECEF velocity range validation (#C2)** on propagated/emitted state vectors.
- **NTNSlice per-CR Prometheus series leak (#178).** The NTNSlice reconciler now
  has a `metrics-cleanup` finalizer that releases its series on deletion:
  `ntn_operators_failover_total` directly, and `ntn_operators_satellite_pass_available`
  (shared by slices in a namespace that reference the same ephemeris) only once
  no other slice in that namespace references it (namespace-scoped refcounting).

### Changed

- **BREAKING (v1alpha1) — `NTNCellConfig.spec.ntn.satSwitchWithResync` redesigned.**
  Replaced the inert `{targetPCI, t304}` (which mapped to nothing in OCUDU and was
  silently dropped) with OCUDU's `sat_switch_with_resync` structure
  (`ntnConfig`, `epochUnixMs`, `tServiceStartUnixMs`, `ssbTimeOffsetSubframes`,
  `gatewayLocation`). It is delivered at runtime, never emitted to static YAML.
- **BREAKING (v1alpha1) — `sibSchedule.siWindowPosition` minimum is now 2** (was 0):
  SIB19 must be scheduled after the mandatory SIB2 entry, so 0/1 produced an
  unbootable config.
- `provider.remoteControl.endpoint` now validates a bare `host:port` (no scheme
  or path): a value like `ws://host:8001` is rejected at admission instead of
  failing to dial and requeuing forever.
- `ntn_operators_failover_total` and `ntn_operators_satellite_pass_available` gain
  a `namespace` label (#178). NTNSlice is namespaced (and its `ephemerisRef` is
  resolved per-namespace), so keying by `slice`/`ephemeris` alone conflated
  same-named CRs/refs across namespaces (and, for the counter, wiped them on
  delete). PromQL that groups by the existing labels still works; add `namespace`
  to dashboards for per-CR accuracy.
- `ntn_operators_gp_satellite_count` gains a `namespace` label (#180), mirroring
  the #178 fix for the SatelliteEphemeris controller. SatelliteEphemeris is
  namespaced, so keying by `ephemeris` (the CR name) alone conflated same-named
  CRs across namespaces and, on delete, the NotFound cleanup wiped the other
  namespace's series (self-healing only at the next ≥2 h GP refresh). The Grafana
  dashboard legend is now `{{namespace}}/{{ephemeris}}`.
- `ntn_operators_ground_station_health` and `ntn_operators_config_apply_errors_total`
  gain a `namespace` label (#183) — both CRDs are namespaced, so keying by
  `station`/`config` (the CR name) alone conflated same-named CRs across
  namespaces. **GroundStationHealth additionally leaked a series on every
  GroundStation deletion**: the write used a composite `station="<ns>.<name>"`
  whose bare-`name` delete never matched — now fixed (plain `station` + a real
  `namespace` label, so the delete matches). Grafana legends/queries updated to
  include `{{namespace}}`.
- `ntn_operators_reader_stale_value_used_total` is now released on NTNSlice
  deletion even when the reconciler falls back to the lazily-built default metrics
  provider — the NotFound evict was previously guarded on the explicit
  `ReaderProvider` field and leaked the series in that (non-production) config (#183).

### Notes

- Issue #53 (`ta-CommonDriftCorrection`, Rel-18) remains tracked-only: OCUDU still
  exposes no parser hook (YAML or runtime) as of this change.
- Known follow-up (tracked, #179): the runtime-push epoch is stamped near-now
  (~5 min) while the ephemeris refresh interval is ≥2 h (GP-source rate limits),
  so a consumer reconcile landing >5 min after the last propagation skips the
  push until the next refresh (the cell keeps serving the last-pushed / bootstrap
  ephemeris). (The other #177 follow-up, NTNSlice metric cleanup, is done in #178.)
- The NTNSlice `metrics-cleanup` finalizer means `kubectl delete ntnslice` now
  completes only after the operator reconciles the deletion; a slice stays
  `Terminating` while the operator is down (standard finalizer behavior).

## [0.5.0] - 2026-05-27

Promotion of `0.5.0-rc.1` after a short soak. No CRD, controller, or
signing-format change vs rc.1; the rc validated the full publish + anonymous
`cosign verify` / `verify-attestation` chain (Rekor logIndex 1635906087),
multi-arch (amd64+arm64), and Helm OCI push. The OLM CSV now declares
`replaces: ntn-operators.v0.4.0` (v0.4.0 is published on OperatorHub) in place
of the rc's `skipRange`.

## [0.5.0-rc.1] - 2026-05-27

### Changed

**NTNCellConfig schema — OCUDU `ntn_config` alignment (#144)**
- `spec.ntn.cellSpecificKoffset` minimum tightened from `0` to `1`. The 3GPP IE
  `cellSpecificKoffset-r17` is `INTEGER(0..1023)` and permits `0`, but OCUDU
  rejects `0` (its CLI and config validation enforce 1-1023), so the CRD mirrors
  the backend rather than the spec. An explicit `cellSpecificKoffset: 0` is now
  rejected at admission; omitting the field still defaults to `150` (the typed
  zero value is dropped by `omitempty`, so only an explicit `0` reaches the
  validation).
- Field documentation now states the unit correctly: the value is in
  **milliseconds** (TS 38.213 / TS 38.300 §16.14.2). OCUDU stores
  `cell_specific_koffset` as `std::chrono::milliseconds` and converts it to
  operating-SCS slots internally, so the integer passes through this operator
  unchanged — no unit conversion. (3GPP expresses K_offset as a slot count
  assuming the 15 kHz reference SCS, where 1 slot = 1 ms; that is only how the
  IE is defined, not a conversion applied here.)
- This brings the CRD into alignment with 7 of the 8 fields in OCUDU's
  `ntn_config` struct (`ntnUlSyncValidityDur`, `polarization`, `taReport`,
  `taInfo.taCommonDrift*`, `epochTime`, `ephemeris*`, and `cellSpecificKoffset`
  were verified present and correctly rendered). The 8th field, `k_mac`, stays
  intentionally deferred pending upstream OCUDU MR!597 reassessment (tracked in
  #52); no schema change for it here.

**Dependencies / CI**
- Bumped the k8s.io stack to **0.36.0** (api, apimachinery, client-go + indirect
  apiextensions-apiserver / apiserver / component-base) with
  `sigs.k8s.io/controller-runtime` **0.23.3 → 0.24.1** (#150). controller-runtime
  0.24 deprecates `controller-runtime/pkg/scheme`, so the api package's scheme
  registration was migrated to the `k8s.io/apimachinery/pkg/runtime.NewSchemeBuilder`
  pattern (current kubebuilder v4 scaffold). No CRD/deepcopy or runtime behavior change.
- Bumped `sigstore/cosign-installer` 3.8.2 → **4.1.2**, with the cosign **binary
  pinned to `v2.6.3`** (#149). Installer v4 defaults to cosign v3.x, which would
  change the published signature/attestation format (OCI 1.1 referrers + new
  protobuf bundle) vs v0.4.0; pinning v2.x keeps the release format-consistent.
  The deliberate cosign v3 migration is tracked in #148.
- Bumped `onsi/ginkgo/v2` 2.28.1 → 2.28.2 (#133), `aquasecurity/trivy-action`
  0.35.0 → 0.36.0 (#127), `docker/login-action` 4.1.0 → 4.2.0 (#145),
  `anchore/sbom-action` 0.18.0 → 0.24.0 (#126, the syft used for the release
  SBOM), and Nephio KRM mutators `set-namespace` 0.4.5 / `set-labels` 0.2.4
  (#118 / #134).

### Added

- E2E suite can run against an existing cluster instead of always provisioning a
  Kind cluster (#135).
- OLM ClusterServiceVersion description and `certified` annotation, with
  `createdAt` aligned to the 2026-04 OperatorHub convention (#124).

## [0.4.0] - 2026-04-27

Promotion of `0.4.0-rc.1` after a 6-day soak. No CRD or controller breaking
changes vs rc.1; all additions are backward compatible.

### Added

**Nephio R6 distribution (#51 / #112)**
- `nephio/packages/ntn-operators-crds` — kpt-installable CRD-only package
- `nephio/packages/ntn-workloads-sample` — kpt-installable sample CRs with
  `set-namespace` + `set-labels` mutator pipeline
- `make nephio-{install-tools,sync,render,validate,verify-sync}` targets
- ADR 0003 — Nephio integration design rationale
- README Quick Start Option D — Nephio install path for first-time visitors

**Supply-chain hardening (#114 / #117)**
- All Kptfile pipeline mutator images pinned by `@sha256:` digest
  (`set-namespace@sha256:f930…`, `set-labels@sha256:cce5…`)
- `hack/check-kptfile-digest-pin.sh` enforces digest pinning, mirroring
  the action-SHA discipline established in `hack/check-action-shas.sh` (#109)
- `test/nephio/validate.sh` T15 wires the check into the validate suite

**CI gating (#119 / #121)**
- `.github/workflows/nephio.yml` — dedicated PR/push gate running
  `make nephio-install-tools` → `nephio-verify-sync` → `nephio-validate`
- T7 / T12 reimplemented as hermetic python-yaml structural checks
  (replaces `kubectl apply --dry-run=client`, which required cluster API
  discovery and failed on hermetic CI runners)

### Changed

- Reconciler 304 status-not-updated bug fix (#110) — SatelliteEphemeris
  controller now correctly persists status when the upstream returns 304
- `pkg/netutil/allowlist.go` — private-host SSRF allow-list, OWASP
  Case 1 hardening (#110)
- E2E uses in-cluster CelesTrak mock to remove network flake (#105 / #110)
- OMM test fixture deduplicated between unit and e2e (#111)

### CI hardening (no operator behavior change)

- E2E path filter narrowed; release.yml dry-run dispatch (#102 / #103 / #104)
- Trivy weekly scheduled scan on main (#108)
- Action SHA validator catches non-SHA refs at PR time (#107 / #109)

### Upgrade notes from 0.4.0-rc.1

- **No CRD or runtime behavior change** vs rc.1 + the #110 reconciler 304 fix
- **OLM upgrade**: `spec.skipRange` is `"<0.4.0"` (was `"<0.4.0-rc.1"`).
  Same reasoning as rc.1 — no rc.1 CSV was published to an OperatorHub
  catalog, so skipRange is the right pattern.

## [0.4.0-rc.1] - 2026-04-21

Release candidate for 0.4.0 consolidating the v0.2 Core Hardening, v0.3
Dynamic Ephemeris, and v0.4 Production Readiness milestones into a single
publishable artifact. No breaking CRD changes vs 0.1.0 — every new field
is optional with the prior behaviour as its default.

### Added

**Dynamic ephemeris (v0.3)**
- SpaceTrack dynamic GP fetcher with Secret-backed credentials (#22, #23)
- SGP4 propagator — OMM → 3GPP ECEF conversion (#37 via #75)
- `NTNCellConfig.spec.ephemerisRef` — re-reconcile on SatelliteEphemeris update (#20)
- `NTNProvider.PushEphemerisUpdate` interface method (#20)

**Failover and metrics (v0.4)**
- Signal-hysteresis dead-band for trigger anti-flapping (#50)
- `NTNSlice.spec.metricsSource` pluggable metrics source — annotations or
  Prometheus, with per-UID stale-cache and reader-layer observability (#67)
- Synthetic `cmd/test-metrics-exporter` for deterministic E2E (#67 / #94)
- `--prometheus-allowed-endpoint-hosts` operator flag — optional SSRF
  allow-list for multi-tenant deployments (#93)
- `MetricsStale` / `EndpointNotAllowed` / `MetricsUnavailable` /
  `MetricsReaderError` failover-ready reasons, each distinct and actionable

**Production readiness (v0.4)**
- Helm NetworkPolicy template (#58)
- Controller benchmarks and `--max-concurrent-reconciles` (#59)
- Container image scanning (Trivy) + cosign signing + SPDX SBOM (#61, #62)
- Validating admission webhook for NTNSlice trigger syntax (#70)
- OLM bundle for OperatorHub publishing (#60; CSV uses `spec.skipRange: "<0.4.0-rc.1"` because v0.1.0 never published a catalog entry)

**Observability**
- `ntn_operators_reader_query_duration_seconds{source,outcome}` histogram
- `ntn_operators_reader_errors_total{source,reason}` counter (5 reasons)
- `ntn_operators_reader_stale_value_used_total{namespace,name}` counter;
  evicted on CR deletion
- Grafana dashboard (6 panels) shipped under `config/grafana/`

**API additions (all backward compatible)**
- SIB19 Stage 2/3 multi-cell NTN fields (#19 via #33 #34)
- `NTNCellConfig.spec.ntn.ephemerisOrbital` alternative (#21 via #30)
- `FailoverPolicy.hysteresisMargin` string field (#50)
- `NTNSlice.spec.metricsSource` block (#67)
- 12 CEL XValidation rules (was 7 at 0.1.0), including `MetricsSource`
  type-requires-prometheus, queries-non-empty, endpoint-URL-pattern

### Changed

- Controller decoupled from OCUDU-specific imports; `NTNProvider` interface
  gained `EnsureOwnership` / `Cleanup` methods (#72)
- Package-level structured logging across all runtime packages (#7, #39)
- NTNSlice reconciler now calls `ReaderProvider.For(ns).Read()` in place
  of the inline `readMetrics()` (#67); behaviour change is gated on
  `spec.metricsSource.type=prometheus` so existing annotation-driven CRs
  continue unchanged

### Fixed

- SpaceTrack credentials no longer interleave across concurrent reconciles (#8)
- `ConfigMapNameFor` adds hash suffix, preventing truncation collisions (#9)
- Firmware version sync drift → `UpdateInterrupted`/`Degraded` transition (#14)
- Global `os.Chdir` in test util removed (#16)
- `nodeToGroundStation` handles hashed labels for long namespace/name (#41)
- Chart.yaml stale `srsran` keyword removed (#42)
- Polarization schema aligned with OCUDU YAML + TS 38.331 enum values (#45)

### Security

- All CI action versions pinned to commit SHA (#17 via #27)
- Dependabot enabled for automated dependency updates (#63)
- SSRF allow-list for user-supplied Prometheus endpoints (#93)
- IP-level SSRF hardening lives in operator Pod NetworkPolicy, not in CRD
  schema — explicit non-goal per design doc `docs/design/metrics-source.md`

### Upgrade notes from 0.1.0

- **CRD schema is backward compatible**. Existing NTNSlice/NTNCellConfig
  CRs remain valid. No conversion webhook required.
- **NTNSlice behaviour** is unchanged unless `spec.metricsSource.type` is
  set to `prometheus`. Annotation-based simulation is still the default.
- **New operator flag** `--prometheus-allowed-endpoint-hosts` defaults to
  empty (permit-all) — existing deployments that did not set it before
  continue to work without change.
- **OLM upgrade** path uses `spec.skipRange: "<0.4.0-rc.1"`, not an
  explicit `replaces:`, because the OLM bundle infrastructure
  landed in PR #91 (after the v0.1.0 tag) so no published
  `ntn-operators.v0.1.0` CSV exists to replace. skipRange lets this
  CSV take over from any earlier-installed bundle if one is ever
  cataloged, without requiring v0.1.0 to exist.
- **External `NTNProvider` implementers** must add three methods
  (`EnsureOwnership`, `Cleanup`, `PushEphemerisUpdate`) to satisfy
  the interface (#20, #72). In practice nobody does — only
  `pkg/provider/ocudu` implements it — but the break is real for
  anyone who built a downstream provider against v0.1.0.

### Known deferrals

- `#49` multi-satellite failover — moved to v0.5 (engineering freeze)
- `#65` OAI gNB provider — moved to v1.0 (1-week+ scope)
- `#68` real antenna readiness probe — moved to v1.0 (requires hardware)
- `#69` SessionContinuity during failover — v1.0, paused
- `#47` / `#52` / `#53` — blocked on upstream OCUDU parser, v1.0

## [0.1.0] - 2026-04-17

### Added
- **4 CRDs**: SatelliteEphemeris, GroundStationLifecycle, NTNCellConfig, NTNSlice
- **CelesTrak GP fetcher**: OMM JSON with ETag caching, SGP4 pass prediction
- **SpaceTrack GP fetcher**: Cookie-based auth, session reuse, Secret credential reading
- **OCUDU provider**: ConfigMap generation for NTN gNB configuration
- **Failover engine**: Terrestrial-satellite path switching with switchback delay
- **QoS/Security/Billing**: Status reporting per active path in NTNSlice
- **CEL validation rules**: URL scheme, lat/lon range, path priority, ECEF non-zero, SpaceTrack credentials
- **Custom Prometheus metrics**: failover_total, satellite_pass_available, ground_station_health, config_apply_errors_total, gp_fetch_duration_seconds, gp_satellite_count
- **Firmware OTA**: Timeout detection (30min), phase stuck recovery
- **Helm chart**: CRDs, RBAC, Deployment, PodDisruptionBudget, ServiceMonitor
- **Release pipeline**: ko multi-arch build, GitHub Actions, Helm OCI push
- **Grafana dashboard**: 6 panels for all custom metrics
- **API reference**: Auto-generated from CRDs via crdoc
- **E2E tests**: Multi-CRD workflow, deletion/finalizer cleanup
- **SSRF protection**: TCP-dial-level IP validation, redirect target validation
- **Security docs**: GOVERNANCE.md, SECURITY.md, CONTRIBUTING.md

### Security
- SSRF-safe HTTP client (private IP blocking at dial level)
- Cross-namespace write prevention (provider.namespace forced to CR namespace)
- ETag cache race condition fix (sync.Map)
- ConfigMap OwnerReference for garbage collection
- CEL validation (no webhook infrastructure needed)
- Secret RBAC for SpaceTrack credentials

### Known Limitations
- Failover metrics are annotation-based (simulated); UPF/Prometheus integration planned for v0.2
- Only OCUDU provider implemented; OAI and Aalyria planned for v0.2
- AntennaReady condition simulated (requires vendor-specific hardware agent)
- SessionContinuity tracked but not enforced at data plane
- Firmware updates require external node agent for actual OTA
