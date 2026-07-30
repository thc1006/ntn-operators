/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ocudu

import (
	"context"
	"crypto/tls"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thc1006/ntn-operators/pkg/provider"
)

// TestPushNTNConfigUpdate_NeverRoutesThroughHTTPProxy pins that the runtime push never honors
// HTTP(S)_PROXY on EITHER the plaintext ws:// or the wss:// path. The gNB endpoint is operator-authored,
// so routing the push through an env proxy would let a CR reach proxy-only networks the Pod itself
// cannot, and make the NetworkPolicy-visible peer the proxy rather than the gNB (proxy-amplified SSRF).
// A capture listener stands in for the proxy; any connection to it is a failure. Mutation: leave the
// plaintext path's HTTPClient.Transport nil (→ http.DefaultTransport → ProxyFromEnvironment) and the
// ws:// case connects to the capture listener, so the count is non-zero.
func TestPushNTNConfigUpdate_NeverRoutesThroughHTTPProxy(t *testing.T) {
	var proxyHits atomic.Int32
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = lis.Close() }()
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			proxyHits.Add(1)
			_ = c.Close()
		}
	}()
	proxyURL := "http://" + lis.Addr().String()
	t.Setenv("HTTP_PROXY", proxyURL)
	t.Setenv("HTTPS_PROXY", proxyURL)
	t.Setenv("NO_PROXY", "") // no host is exempt from the (would-be) proxy
	t.Setenv("no_proxy", "")

	env, err := buildNTNConfigUpdate(ecefRuntimeUpdate())
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}

	// A non-loopback, unresolvable endpoint: ProxyFromEnvironment WOULD proxy it (loopback is
	// implicitly exempt, so a 127.0.0.1 target could not detect the leak). A direct dial fails at DNS
	// and never touches the proxy; a proxied dial connects to the capture listener first.
	targets := []struct {
		name   string
		target provider.ResolvedRemoteControl
	}{
		{"plaintext-ws", provider.ResolvedRemoteControl{Endpoint: "gnb-proxy-probe.invalid:8001"}},
		{"tls-wss", provider.ResolvedRemoteControl{
			Endpoint:  "gnb-proxy-probe.invalid:8001",
			TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}},
	}
	for _, tc := range targets {
		// The dial is expected to FAIL (unresolvable host); only the proxy-usage matters here.
		_ = pushNTNConfigUpdate(context.Background(), tc.target, env)
	}
	time.Sleep(100 * time.Millisecond) // let any wrong proxy Accept land on the listener goroutine
	if got := proxyHits.Load(); got != 0 {
		t.Fatalf("runtime push must never route through HTTP(S)_PROXY; capture proxy saw %d connection(s)", got)
	}
}
