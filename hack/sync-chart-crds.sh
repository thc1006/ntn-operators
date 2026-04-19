#!/usr/bin/env bash
# Copy controller-gen CRDs from config/crd/bases/ into the Helm chart at
# dist/chart/templates/crd/, re-adding the Helm templating wrappers the chart
# needs to respect `crd.enable` / `crd.keep` user values.
#
# The wrappers are:
#   1. {{- if .Values.crd.enable }} / {{- end }} around the whole CRD
#   2. {{ if .Values.crd.keep }} "helm.sh/resource-policy": keep {{ end }}
#      injected under metadata.annotations
#
# Without (1), users cannot opt out of CRD installation via `helm install
# --set crd.enable=false`. Without (2), users lose the keep-on-uninstall
# protection and `helm uninstall` would cascade-delete every CR — data loss.
#
# Usage: invoked from `make manifests` after controller-gen runs.

set -euo pipefail

SRC_DIR="config/crd/bases"
DEST_DIR="dist/chart/templates/crd"

[ -d "$SRC_DIR" ] || { echo "missing $SRC_DIR"; exit 1; }
[ -d "$DEST_DIR" ] || { echo "missing $DEST_DIR"; exit 1; }

for src in "$SRC_DIR"/*.yaml; do
	# config/crd/bases/ntn.operators.dev_<kind>s.yaml -> <kind>s.ntn.operators.dev.yaml
	base=$(basename "$src" | sed -E 's/^ntn\.operators\.dev_(.+)\.yaml$/\1.ntn.operators.dev.yaml/')
	dest="$DEST_DIR/$base"

	# Only touch CRDs already present in the chart (don't introduce new ones here).
	[ -f "$dest" ] || continue

	awk '
		BEGIN { wrapped = 0 }
		# Replace leading `---` with the Helm enable guard.
		NR == 1 && /^---$/ {
			print "{{- if .Values.crd.enable }}"
			wrapped = 1
			next
		}
		# If the file somehow did not start with `---`, still prepend the guard.
		NR == 1 && /^apiVersion:/ {
			print "{{- if .Values.crd.enable }}"
			print
			wrapped = 1
			next
		}
		# Inject the keep annotation as the first entries under metadata.annotations.
		/^  annotations:$/ {
			print
			print "    {{ if .Values.crd.keep }}"
			print "    \"helm.sh/resource-policy\": keep"
			print "    {{ end }}"
			next
		}
		{ print }
		END {
			if (wrapped) print "{{- end }}"
		}
	' "$src" > "$dest"
done
