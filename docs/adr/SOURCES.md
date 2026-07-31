# Source and verification register

Verification date: **2026-07-31**

The sources below are prioritized in this order: normative standard, official
project documentation/API, upstream source, repository source/tests, vendor
primary documentation. A source appearing here does not mean every statement
in it is accepted; the ADRs state the limited fact used.

## Kubernetes API and security

1. Kubernetes — Versions in CustomResourceDefinitions  
   https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/

2. Kubernetes — Storage Version Migration  
   https://kubernetes.io/docs/tasks/manage-kubernetes-objects/storage-version-migration/

3. Kubernetes — Common Expression Language  
   https://kubernetes.io/docs/reference/using-api/cel/

4. Kubernetes — CustomResourceDefinition validation  
   https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/

5. Kubernetes — ValidatingAdmissionPolicy  
   https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/

6. Kubernetes — ValidatingAdmissionPolicy v1 API  
   https://kubernetes.io/docs/reference/kubernetes-api/admissionregistration-resources/validating-admission-policy-v1/

7. Kubernetes — Application Security Checklist  
   https://kubernetes.io/docs/concepts/security/application-security-checklist/

8. Kubernetes API conventions / conditions  
   https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md

9. Kubernetes liveness, readiness and startup probes  
   https://kubernetes.io/docs/concepts/configuration/liveness-readiness-startup-probes/

## 3GPP / mobility

10. 3GPP TS 38.331 report page  
    https://www.3gpp.org/dynareport/38331.htm

11. ETSI TS 138 331 archive  
    https://www.etsi.org/deliver/etsi_ts/138300_138399/138331/

## Nephio / kpt

12. Nephio documentation and release notes  
    https://docs.nephio.org/  
    https://docs.nephio.org/docs/release-notes/

13. Porch releases/source  
    https://github.com/nephio-project/porch

14. kpt `pkg get` and `fn render`  
    https://kpt.dev/reference/cli/pkg/get/  
    https://kpt.dev/reference/cli/fn/render/

15. Nephio catalog  
    https://github.com/nephio-project/catalog

## Orbital data

16. CelesTrak  
    https://celestrak.org/

17. Space-Track  
    https://www.space-track.org/

## Ground-station health

18. CCSDS 902.11-R-1 Cross Support Service Management best-practices review  
    https://ccsds.org/review/902-11-r-1/

19. NASA DSN Now real-time ground-station status  
    https://eyes.jpl.nasa.gov/apps/dsn-now/dsn.html

20. SatService ACU2-RMU manual  
    https://satservicegmbh.de/files/satnms/doc/acu2-rmu/index.html

21. SignalRange ACU documentation  
    https://docs.signalrange.space/equipment/antenna-control-unit/

22. GISS antenna control unit / built-in self-test  
    https://giss-satcom.com/en/terminals/flyaway-class-terminals/acu-antenna-control-unit

23. Alignsat antenna control system  
    https://www.alignsat.com/products/alignsat-39107cd-antenna-control-system-82

## Repository evidence

24. GroundStationLifecycle API  
    https://github.com/thc1006/ntn-operators/blob/main/api/v1alpha1/groundstationlifecycle_types.go

25. GroundStationLifecycle controller  
    https://github.com/thc1006/ntn-operators/blob/main/internal/controller/groundstationlifecycle_controller.go

26. SatelliteEphemeris API (`refreshInterval`)  
    https://github.com/thc1006/ntn-operators/blob/main/api/v1alpha1/satelliteephemeris_types.go

27. Existing duration CEL rules  
    https://github.com/thc1006/ntn-operators/blob/main/api/v1alpha1/ntnslice_types.go

28. Runtime WebSocket client  
    https://github.com/thc1006/ntn-operators/blob/main/pkg/provider/ocudu/wsclient.go
