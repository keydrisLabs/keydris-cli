package proxyscope

import "testing"

func TestNormalize(t *testing.T) {
	tests := map[string]string{
		"Example.COM":               "example.com:443",
		"example.com.":              "example.com:443",
		"http://example.com":        "example.com:80",
		"https://example.com:8443/": "example.com:8443",
		"127.0.0.1:3000":            "127.0.0.1:3000",
		"[2001:db8::1]:443":         "[2001:db8::1]:443",
		"2001:db8::1":               "[2001:db8::1]:443",
		"example.com:0443":          "example.com:443",
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			got, err := Normalize(input)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("Normalize(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestNormalizeRejectsPaths(t *testing.T) {
	if _, err := Normalize("https://example.com/api"); err == nil {
		t.Fatal("expected path rejection")
	}
}

func TestScopeSelected(t *testing.T) {
	scope, err := New(ModeSelected, []string{"Example.COM"})
	if err != nil {
		t.Fatal(err)
	}
	if !scope.Managed("example.com:443") {
		t.Fatal("selected destination should be managed")
	}
	if scope.Managed("api.example.com:443") {
		t.Fatal("unlisted destination should pass through")
	}
}

func TestScopeAll(t *testing.T) {
	scope, err := New(ModeAll, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !scope.Managed("anything.example:443") {
		t.Fatal("all mode should manage every valid destination")
	}
}
