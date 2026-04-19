# Migration: v0.2 → v0.3 (polarization schema change)

## What changed

`NTNCellConfig.spec.ntn.polarization` changed from a flat string enum to a
nested object with independent downlink and uplink fields.

**v0.2 (pre-change):**

```yaml
spec:
  ntn:
    polarization: linear    # or "circular"
```

**v0.3 (current):**

```yaml
spec:
  ntn:
    polarization:
      dl: linear            # rhcp | lhcp | linear
      ul: linear            # rhcp | lhcp | linear
```

## Why

- 3GPP TS 38.331 defines `ntn-PolarizationDL-r17` and `ntn-PolarizationUL-r17`
  as two independent IEs in SIB19 — a single scalar cannot express the
  downlink / uplink asymmetry real LEO payloads exhibit.
- OCUDU's YAML writer (`du_high_ntn_config_yaml_writer.cpp`) emits the
  nested form (and the CLI11 schema aligns with that shape), so the old
  scalar schema produced configs that
  the gNB silently ignored or failed to load.
- The v0.2 enum value `"circular"` was not in OCUDU's accepted set
  (`rhcp | lhcp | linear`) and would have failed at runtime anyway.

## Migration value mapping

| v0.2 `polarization` | v0.3 `polarization.dl` | v0.3 `polarization.ul` | Note |
|---|---|---|---|
| `linear` | `linear` | `linear` | Direct carry-over |
| `circular` | `rhcp` | `rhcp` | Right-hand assumed; override if your payload uses LHCP |
| (unset) | — | — | Omit the field entirely; OCUDU falls back to its default |

## Migration scripts (sed + jq)

If you have existing `NTNCellConfig` manifests in git or on disk:

```bash
# Bulk-convert all YAML files in a directory.
for f in $(grep -E -rl '^[[:space:]]*polarization:[[:space:]]*(linear|circular)[[:space:]]*$' config/); do
  # linear → {dl: linear, ul: linear}
  sed -i.bak 's/^\([[:space:]]*\)polarization:[[:space:]]*linear[[:space:]]*$/\1polarization:\n\1  dl: linear\n\1  ul: linear/' "$f"
  # circular → {dl: rhcp, ul: rhcp} (assume RHCP; review before committing)
  sed -i.bak 's/^\([[:space:]]*\)polarization:[[:space:]]*circular[[:space:]]*$/\1polarization:\n\1  dl: rhcp\n\1  ul: rhcp/' "$f"
  rm -f "$f.bak"
done
```

For cluster-stored CRs, use a one-off kubectl/jq pipeline:

```bash
kubectl get ntncc -A -o json \
  | jq -r '.items[] | select(.spec.ntn.polarization | type == "string")
           | "kubectl patch ntncc \(.metadata.name) -n \(.metadata.namespace) --type=json -p "
             + (
                 if .spec.ntn.polarization == "linear"
                 then "''[{\"op\":\"replace\",\"path\":\"/spec/ntn/polarization\",\"value\":{\"dl\":\"linear\",\"ul\":\"linear\"}}]''"
                 else "''[{\"op\":\"replace\",\"path\":\"/spec/ntn/polarization\",\"value\":{\"dl\":\"rhcp\",\"ul\":\"rhcp\"}}]''"
                 end
               )' \
  | sh
```

Review the RHCP/LHCP choice for your specific payload before applying.

## Upgrade order

1. Apply the new CRDs first (`kubectl apply -k config/crd` or
   `helm upgrade ntn-operators`). The new schema is permissive enough to
   accept unset `polarization`, so existing CRs with `polarization` unset
   continue to apply cleanly.
2. Run the migration script against any CR that still uses the flat form.
3. Deploy the v0.3 controller image. The controller will re-render each
   CR's ConfigMap with the nested `polarization` YAML.
4. Restart the OCUDU gNB so it picks up the regenerated ConfigMap.
