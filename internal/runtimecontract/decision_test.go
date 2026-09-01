package runtimecontract

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"strings"
	"testing"
)

//go:embed bundle/v1/fixtures/decision-approval-required.json
var approvalRequiredDecisionFixture string

//go:embed bundle/v1/schemas/decision-response.schema.json
var canonicalDecisionResponseSchema string

const approvalRequiredDecisionFixtureSHA256 = "06d5c612260357ddc6dc7cc600d180f5e600076843bdf90d3f2b02477bb6c7bb"
const canonicalDecisionResponseSchemaSHA256 = "c7a741013c59ab7c70ac0ace846b9bf8bcaaafaca1fcc3a4c99cb60930cbcb1a"

func TestVendoredDecisionArtifactsChecksums(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{name: "fixture", contents: approvalRequiredDecisionFixture, want: approvalRequiredDecisionFixtureSHA256},
		{name: "schema", contents: canonicalDecisionResponseSchema, want: canonicalDecisionResponseSchemaSHA256},
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

func TestDecodeDecisionResponseApprovalRequired(t *testing.T) {
	response, err := DecodeDecisionResponse(strings.NewReader(approvalRequiredDecisionFixture))
	if err != nil {
		t.Fatal(err)
	}
	if response.Decision != DecisionApprovalRequired {
		t.Fatalf("decision = %q", response.Decision)
	}
}

func TestDecodeDecisionResponseRejectsContractDrift(t *testing.T) {
	tests := map[string]string{
		"legacy enum": strings.Replace(
			approvalRequiredDecisionFixture,
			`"approval_required"`,
			`"require_approval"`,
			1,
		),
		"mismatched reason": strings.Replace(
			approvalRequiredDecisionFixture,
			`"keydris_approval_required"`,
			`"keydris_policy_allowed"`,
			1,
		),
		"unknown field": strings.Replace(
			approvalRequiredDecisionFixture,
			`"schema_version": 1,`,
			`"schema_version": 1, "extra": true,`,
			1,
		),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeDecisionResponse(strings.NewReader(body)); err == nil {
				t.Fatal("invalid response was accepted")
			}
		})
	}
}
