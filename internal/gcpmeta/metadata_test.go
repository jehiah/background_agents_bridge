package gcpmeta

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient points a Client at the given test server.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	t.Setenv(hostEnv, strings.TrimPrefix(srv.URL, "http://"))
	return NewClient()
}

func TestInstanceAttribute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(flavorHeader) != flavorValue {
			t.Errorf("missing request header %s", flavorHeader)
		}
		w.Header().Set(flavorHeader, flavorValue)
		switch r.URL.Path {
		case "/computeMetadata/v1/instance/attributes/sandbox-id":
			w.Write([]byte("sb-123\n"))
		case "/computeMetadata/v1/instance/attributes/missing":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	got, err := c.InstanceAttribute(t.Context(), "sandbox-id")
	if err != nil {
		t.Fatalf("InstanceAttribute: %v", err)
	}
	if got != "sb-123" {
		t.Errorf("value = %q, want %q (trimmed)", got, "sb-123")
	}

	// Missing attribute is ("", nil), not an error.
	got, err = c.InstanceAttribute(t.Context(), "missing")
	if err != nil || got != "" {
		t.Errorf("missing attribute = (%q, %v), want (\"\", nil)", got, err)
	}
}

func TestInstanceAttributeServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(flavorHeader, flavorValue)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, err := c.InstanceAttribute(t.Context(), "x"); err == nil {
		t.Fatal("expected error on HTTP 502")
	}
}

func TestRejectsNonMetadataResponse(t *testing.T) {
	// A server that omits the Metadata-Flavor response header (e.g. a captive
	// portal) must be rejected.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not really metadata"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, err := c.InstanceAttribute(t.Context(), "x"); err == nil {
		t.Fatal("expected error when Metadata-Flavor response header is absent")
	}
}
