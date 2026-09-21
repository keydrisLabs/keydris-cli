package meter

import "testing"

func TestOriginsBuiltinsAndOverrides(t *testing.T) {
	origins := NewOrigins([]string{
		"llm.internal.example=custom",
		"api.openai.com=off",
	})

	if provider, ok := origins.Provider("api.anthropic.com", 443); !ok || provider != "anthropic" {
		t.Fatalf("anthropic builtin: %q %v", provider, ok)
	}
	if provider, ok := origins.Provider("API.ANTHROPIC.COM", 443); !ok || provider != "anthropic" {
		t.Fatalf("host matching must be case-insensitive: %q %v", provider, ok)
	}
	if _, ok := origins.Provider("api.anthropic.com", 8443); ok {
		t.Fatal("only port 443 is metered")
	}
	if _, ok := origins.Provider("api.openai.com", 443); ok {
		t.Fatal("an =off override must remove a builtin")
	}
	if provider, ok := origins.Provider("llm.internal.example", 443); !ok || provider != "custom" {
		t.Fatalf("override: %q %v", provider, ok)
	}
	if _, ok := origins.Provider("api.github.com", 443); ok {
		t.Fatal("unlisted origins are never metered")
	}
}

// TestOriginsMeterOnlyProviderAPIs pins the metered set after the Codex revert:
// chatgpt.com is not a provider API origin, so ChatGPT-backend traffic is
// policy-routed or tunneled and never handed to the usage parser.
func TestOriginsMeterOnlyProviderAPIs(t *testing.T) {
	origins := NewOrigins(nil)
	if provider, ok := origins.Provider("chatgpt.com", 443); ok {
		t.Fatalf("chatgpt.com must not be metered, got provider %q", provider)
	}
	if provider, ok := origins.Provider("api.openai.com", 443); !ok || provider != "openai" {
		t.Fatalf("api.openai.com = %q, %v", provider, ok)
	}
}
