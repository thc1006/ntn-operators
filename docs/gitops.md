# GitOps compatibility

The NTN operators are designed to coexist cleanly with GitOps controllers
(Argo CD, Flux). This note explains why and how to keep drift detection quiet.

## The operator never writes to `.spec`

Each reconciler treats `.spec` as user (Git) intent and never mutates it. The
only writes to a CR object other than its `/status` subresource are finalizer
add/remove operations on `.metadata`. When the Git manifest does not declare those
controller-owned finalizers, a normal server-side-apply / three-way-merge sync
neither owns nor reverts them; a `Replace`/force sync strategy, or a manifest that
itself declares finalizers, can behave differently. Derived state on the CR is
exposed through `.status`; separately, the operator also produces provider
artifacts (generated ConfigMaps) and runtime gNB pushes — operator-managed outputs
covered in [Do not GitOps-manage operator-generated artifacts](#do-not-gitops-manage-operator-generated-artifacts).

Because the operator is not a second writer to any spec field, Argo CD / Flux
never fight it for ownership of `.spec`.

## Defaults are applied by the API server, not the controller

Field defaults use CRD schema defaults (`+kubebuilder:default=`), which the API
server applies on create/update and **normally persists**, so they are visible in
`kubectl get -o yaml` and legible to GitOps rather than being an invisible
controller side effect. (One nuance: a default introduced *after* an object was
already stored is applied on read but not written back until the object's next
update — so a just-added default can appear in a `get` before it is persisted.
This affects only newly-added defaults, not steady state.) The operator itself
does not perform controller-side spec defaulting.

## Computed values live in `.status`

Derived/effective configuration — the applied K_offset, the resolved ephemeris
(ECEF) pushed to the gNB, effective cell parameters, pass windows, failover
state — is written to `.status`, never back into `.spec`. Argo CD excludes
`status` from diffing by default, and Flux does not manage it, so these values
never register as drift.

## Do not GitOps-manage operator-generated artifacts

Manage the NTN custom resources (the CRs' `.spec`) in Git. Do **not** also commit
or reconcile the artifacts the operator generates from them:

- **Generated ConfigMaps.** `NTNCellConfig`'s OCUDU provider renders the gNB NTN
  config (e.g. `geo_ntn.yml`) into a ConfigMap, sets a controller `OwnerReference`
  back to the CR, and keeps its `data`/annotations in sync every reconcile. These
  ConfigMaps are operator-owned outputs, identifiable by their controller
  OwnerReference and the labels `app.kubernetes.io/managed-by=ntn-operators` and
  `app.kubernetes.io/component=ocudu-ntn-config`.
- **Runtime gNB pushes.** The SGP4→ECEF ephemeris and NTN parameters pushed to a
  live gNB are controller side effects, not Git-managed Kubernetes objects.

If the same generated ConfigMap is placed under Argo CD or Flux management, the
GitOps controller reconciles it back to the Git copy while the operator reconciles
it back to the CR-derived copy, and the two overwrite each other on every cycle.
Observe these outputs through the CR's `.status`, conditions, events, and metrics
instead of managing them in Git. In practice, simply not rendering the generated
ConfigMap into the GitOps source is enough — a resource that exists only in the
cluster, absent from the desired manifests, is not something Argo CD or Flux
adopts and reconciles. If a repository render accidentally emits it, drop it at the
source / Kustomize path level. (Live-resource exclusion mechanisms differ per tool
— Argo CD's global `resource.exclusions` filters by apiGroup/kind, not by label,
and Flux's label-scoped `.spec.ignore` governs drift on already-managed resources —
so a label-based exclusion is not a portable instruction.)

## Recommended: server-side diff / apply

Schema defaults added by the API server can show up as a spurious diff under the
**legacy** client-side 3-way merge. The clean, systemic fix is to let the API
server add the same defaults to the desired manifest before comparison:

- **Argo CD** — enable [Server-Side Diff](https://argo-cd.readthedocs.io/en/stable/user-guide/diff-strategies/)
  (`ServerSideDiff=true`; beta since Argo CD 2.10, stable from 3.1). The dry-run
  server-side apply makes apiserver defaults match on both sides, eliminating false diffs.
- **Flux** — the kustomize-controller reconciles with
  [Server-Side Apply and periodic drift correction](https://fluxcd.io/flux/components/kustomize/kustomizations/#drift-detection).
  Fields ABSENT from the desired manifest are generally left to whichever field
  manager owns them; fields that ARE in the desired manifest are still reconciled
  back per SSA ownership and Flux's policy (default `Override`), so "owned by another
  manager" is not a blanket exemption. Flux also supports field-scoped drift ignore,
  usually preferable to ignoring a whole object.

`ignoreDifferences` (Argo CD) and Flux's `kustomize.toolkit.fluxcd.io/ssa: Ignore`
annotation are legitimate for a field another controller genuinely owns. The
anti-pattern is a BLANKET ignore of a whole object or a large subtree, which hides
real drift — prefer a field-scoped ignore, or the server-side diff/apply above.
