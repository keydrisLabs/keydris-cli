package runtimecontract

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestUsageContractBundle(t *testing.T) {
	raw, err := os.ReadFile("bundle/usage-v1/source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Version   string `json:"contract_bundle_version"`
		Artifacts map[string]struct {
			SHA256 string `json:"sha256"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	if source.Version != UsageContractBundleVersion {
		t.Fatal(source.Version)
	}
	for filename, artifact := range source.Artifacts {
		body, err := os.ReadFile("bundle/usage-v1/" + filename)
		if err != nil {
			t.Fatal(err)
		}
		if actual := fmt.Sprintf("%x", sha256.Sum256(body)); actual != artifact.SHA256 {
			t.Fatalf("%s checksum mismatch", filename)
		}
	}
	body, err := os.ReadFile("bundle/usage-v1/session-usage-report.json")
	if err != nil {
		t.Fatal(err)
	}
	var report SessionUsageReport
	if err := decodeStrict(body, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Events) != 2 || report.Events[1].ServiceTier != "default" || report.Events[1].OutputTokens != nil {
		t.Fatalf("%+v", report)
	}
}
