#!/usr/bin/env bash
# TDD test for NetworkPolicy Helm template rendering.
# Usage: hack/test-networkpolicy-template.sh
set -euo pipefail

CHART_DIR="dist/chart"
ERRORS=0

fail() { echo "FAIL: $1"; ERRORS=$((ERRORS + 1)); }

echo "=== NetworkPolicy Helm Template Tests ==="

# Test 1: NetworkPolicy is NOT rendered when disabled (default)
output=$(helm template test "$CHART_DIR" 2>/dev/null)
if echo "$output" | grep -q 'kind: NetworkPolicy'; then
  fail "NetworkPolicy should not render when networkPolicy.enable is false (default)"
fi

# Test 2: NetworkPolicy IS rendered when enabled
output=$(helm template test "$CHART_DIR" --set networkPolicy.enable=true 2>/dev/null)
if ! echo "$output" | grep -q 'kind: NetworkPolicy'; then
  fail "NetworkPolicy should render when networkPolicy.enable=true"
fi

# Test 3: Pod selector targets controller-manager
if ! echo "$output" | grep -A5 'podSelector' | grep -q 'control-plane: controller-manager'; then
  fail "podSelector should match control-plane: controller-manager"
fi

# Test 4: Both Ingress and Egress policy types declared
if ! echo "$output" | grep -qF -- '- Ingress'; then
  fail "policyTypes should include Ingress"
fi
if ! echo "$output" | grep -qF -- '- Egress'; then
  fail "policyTypes should include Egress"
fi

# Test 5: Ingress allows metrics port 8443
if ! echo "$output" | grep -q '8443'; then
  fail "ingress should allow port 8443 (metrics)"
fi

# Test 6: Ingress allows health probe port 8081
if ! echo "$output" | grep -q '8081'; then
  fail "ingress should allow port 8081 (health probes)"
fi

# Test 7: Egress allows HTTPS port 443 (not just 8443)
if ! echo "$output" | grep -qF 'port: 443'; then
  fail "egress should allow port 443 (HTTPS)"
fi

# Test 8: Egress allows DNS port 53 (exact match)
if ! echo "$output" | grep -qF 'port: 53'; then
  fail "egress should allow port 53 (DNS)"
fi

if [ "$ERRORS" -eq 0 ]; then
  echo "PASS: all 8 tests passed."
else
  echo "FAIL: $ERRORS test(s) failed."
  exit 1
fi
