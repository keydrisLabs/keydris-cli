package runtimecontract

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"strings"
	"testing"
)

//go:embed bundle/v1/fixtures/kit-session-response.json
var canonicalKITSession string

//go:embed bundle/v1/schemas/kit-session-response.schema.json
var canonicalKITSessionSchema string

const canonicalKITSessionSHA256 = "b720b30ad7b4a63f491d4fa777a16ff7b76a4b3532c8ec3d0a665757387bf5cd"
const canonicalKITSessionSchemaSHA256 = "32f2645f758018bc93ce5e6f89d61a1e20c58839f0ece16b5924db5edc9f631d"

func TestVendoredKitSessionArtifactsChecksums(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{name: "fixture", contents: canonicalKITSession, want: canonicalKITSessionSHA256},
		{name: "schema", contents: canonicalKITSessionSchema, want: canonicalKITSessionSchemaSHA256},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := fmt.Sprintf("%x", sha256.Sum256([]byte(test.contents)))
			if actual != test.want {
				t.Fatalf("vendored %s checksum = %s, want %s", test.name, actual, test.want)
			}
		})
	}
}

func TestDecodeKitSession(t *testing.T) {
	session, err := DecodeKitSession(strings.NewReader(canonicalKITSession))
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID != "01K1X4Y5Z6A7B8C9D0E1F2G3H4" {
		t.Fatalf("session id = %q", session.SessionID)
	}
	if session.KIT != "fixture.jwt-svid.signature" {
		t.Fatalf("KIT = %q", session.KIT)
	}
}

func TestDecodeKitSessionRejectsLegacyResponse(t *testing.T) {
	_, err := DecodeKitSession(strings.NewReader(
		`{"spiffe_id":"spiffe://keydris.test/run","svid":"legacy","ulid":"01K1X4Y5Z6A7B8C9D0E1F2G3H4","expires_at":"2026-07-28T12:15:00Z"}`,
	))
	if err == nil {
		t.Fatal("legacy response unexpectedly accepted")
	}
}

func TestDecodeKitSessionRejectsDuplicateKeys(t *testing.T) {
	duplicate := strings.Replace(
		canonicalKITSession,
		`"schema_version": 1`,
		`"schema_version": 1, "schema_version": 1`,
		1,
	)
	_, err := DecodeKitSession(strings.NewReader(duplicate))
	if err == nil || !strings.Contains(err.Error(), "duplicate object key") {
		t.Fatalf("duplicate-key error = %v", err)
	}
}
