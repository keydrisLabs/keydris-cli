package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessTokenLoginRejectsInvalidStdinBeforeEnrollment(t *testing.T) {
	cfg := uxConfig(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	cfg.ControlURL = server.URL
	for _, input := range []io.Reader{
		strings.NewReader(""),
		strings.NewReader(" \n"),
		strings.NewReader("first\nsecond"),
		strings.NewReader(strings.Repeat("x", 64*1024+1)),
	} {
		if accessTokenLogin(cfg, input) == 0 {
			t.Fatal("invalid stdin was accepted")
		}
	}
	if requests != 0 {
		t.Fatal("invalid stdin was sent to the enrollment endpoint")
	}
}
