#!/usr/bin/env bash
# TDD test for NetworkPolicy Helm template rendering.
# Usage: hack/test-networkpolicy-template.sh
set -euo pipefail

CHART_DIR="dist/chart"
ERRORS=0

fail() { echo "FAIL: $1"; ERRORS=$((ERRORS + 1)); }

# Extract only the NetworkPolicy document from helm template output.
extract_np() {
  python3 -c "
import sys, yaml
for doc in yaml.safe_load_all(sys.stdin):
    if doc and doc.get('kind') == 'NetworkPolicy':
        print(yaml.dump(doc, default_flow_style=False))
        break
"
}

echo "=== NetworkPolicy Helm Template Tests ==="

# Test 1: NetworkPolicy is NOT rendered when disabled (default)
output=$(helm template test "$CHART_DIR")
if echo "$output" | grep -q 'kind: NetworkPolicy'; then
  fail "NetworkPolicy should not render when networkPolicy.enable is false (default)"
fi

# Test 2: NetworkPolicy IS rendered when enabled — extract only the NP document
np=$(helm template test "$CHART_DIR" --set networkPolicy.enable=true | extract_np)
if [ -z "$np" ]; then
  fail "NetworkPolicy should render when networkPolicy.enable=true"
  echo "FAIL: 1 test(s) failed (cannot continue without NetworkPolicy document)."
  exit 1
fi

# Test 3: Pod selector targets controller-manager
if ! echo "$np" | grep -q 'control-plane: controller-manager'; then
  fail "podSelector should match control-plane: controller-manager"
fi

# Test 4: Both Ingress and Egress policy types declared
if ! echo "$np" | grep -qF -- '- Ingress'; then
  fail "policyTypes should include Ingress"
fi
if ! echo "$np" | grep -qF -- '- Egress'; then
  fail "policyTypes should include Egress"
fi

# Test 5: Ingress allows metrics port 8443
if ! echo "$np" | grep -qF 'port: 8443'; then
  fail "ingress should allow port 8443 (metrics)"
fi

# Test 6: Ingress allows health probe port 8081
if ! echo "$np" | grep -qF 'port: 8081'; then
  fail "ingress should allow port 8081 (health probes)"
fi

# Test 7: Egress allows HTTPS port 443
if ! echo "$np" | grep -qF 'port: 443'; then
  fail "egress should allow port 443 (HTTPS)"
fi

# Test 8: Egress allows DNS port 53
if ! echo "$np" | grep -qF 'port: 53'; then
  fail "egress should allow port 53 (DNS)"
fi

if [ "$ERRORS" -eq 0 ]; then
  echo "PASS: all 8 tests passed."
else
  echo "FAIL: $ERRORS test(s) failed."
  exit 1
fi
