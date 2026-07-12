/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ntnv1alpha1 "github.com/thc1006/ntn-operators/api/v1alpha1"
	"github.com/thc1006/ntn-operators/pkg/ephemeris"
)

// TestClassifyFetchError_InvalidSourceURLIsPermanent pins #222 review P2: an invalid source
// URL is a PERMANENT config error and must not be returned as a transient error (which would
// drive the workqueue's exponential-backoff loop). Mutation: drop the ErrInvalidSourceURL
// case → it falls to the default FetchFailed (returnAsError=true).
func TestClassifyFetchError_InvalidSourceURLIsPermanent(t *testing.T) {
	err := fmt.Errorf("%w: off-origin", ephemeris.ErrInvalidSourceURL)
	reason, requeueAfter, returnAsError := classifyFetchError(err, 4*time.Hour)
	if reason != "InvalidSourceURL" {
		t.Errorf("reason = %q, want InvalidSourceURL", reason)
	}
	if returnAsError {
		t.Error("an invalid source URL must be classified permanent (not returned as a transient error)")
	}
	if requeueAfter != 4*time.Hour {
		t.Errorf("requeueAfter = %s, want the slow refresh interval", requeueAfter)
	}
}

// TestFetcherForSource_SpaceTrackSecretErrorsAreUniform pins #222 review blocker 3: a missing
// Secret and a missing key must produce the SAME CR-facing error, so a principal who can write
// the CR but not read Secrets cannot probe Secret existence or key shape. Mutation: revert to
// the differentiated per-cause messages → the two error strings differ.
func TestFetcherForSource_SpaceTrackSecretErrorsAreUniform(t *testing.T) {
	sch := makeScheme(t)
	if err := corev1.AddToScheme(sch); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	ctx := context.Background()
	stFetcher := ephemeris.NewSpaceTrackFetcher(&http.Client{}, "https://www.space-track.org")
	mkEph := func(secretName string) *ntnv1alpha1.SatelliteEphemeris {
		return &ntnv1alpha1.SatelliteEphemeris{
			ObjectMeta: metav1.ObjectMeta{Name: "eph", Namespace: "default"},
			Spec: ntnv1alpha1.SatelliteEphemerisSpec{
				Source: ntnv1alpha1.EphemerisSource{
					Type: "SpaceTrack", URL: "https://www.space-track.org/basicspacedata/query",
					Credentials: &ntnv1alpha1.SecretReference{Name: secretName, Key: "password"},
				},
			},
		}
	}

	// Case 1: the referenced Secret does not exist.
	r1 := &SatelliteEphemerisReconciler{
		Client: fake.NewClientBuilder().WithScheme(sch).Build(), Scheme: sch, SpaceTrackFetcher: stFetcher,
	}
	_, errMissingSecret := r1.fetcherForSource(ctx, mkEph("no-such-secret"))

	// Case 2: the Secret exists but lacks the password key.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data:       map[string][]byte{"username": []byte("u")}, // no "password"
	}
	r2 := &SatelliteEphemerisReconciler{
		Client: fake.NewClientBuilder().WithScheme(sch).WithObjects(secret).Build(), Scheme: sch, SpaceTrackFetcher: stFetcher,
	}
	_, errMissingKey := r2.fetcherForSource(ctx, mkEph("creds"))

	if errMissingSecret == nil || errMissingKey == nil {
		t.Fatalf("both credential failures must error: secret=%v key=%v", errMissingSecret, errMissingKey)
	}
	if errMissingSecret.Error() != errMissingKey.Error() {
		t.Fatalf("Secret-missing and key-missing must produce the SAME CR-facing message (no oracle):\n"+
			"  missing secret: %q\n  missing key:    %q", errMissingSecret.Error(), errMissingKey.Error())
	}
	if !errors.Is(errMissingSecret, errSpaceTrackCredentialUnavailable) {
		t.Errorf("expected the uniform errSpaceTrackCredentialUnavailable, got %v", errMissingSecret)
	}
}
