package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type fakeDatabaseSource struct {
	catalog *Catalog
	status  dbStatus
}

func (f fakeDatabaseSource) Snapshot() *Catalog {
	return f.catalog
}

func (f fakeDatabaseSource) Status() dbStatus {
	return f.status
}

func testHTTPHandler(t *testing.T) http.Handler {
	t.Helper()
	return newHTTPHandler(fakeDatabaseSource{
		catalog: testCatalog(t),
		status: dbStatus{
			State:   "ready",
			HasData: true,
			Message: "Database ready.",
		},
	}, log.New(io.Discard, "", 0))
}

func TestCountriesEndpoint(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/countries", nil)
	recorder := httptest.NewRecorder()
	testHTTPHandler(t).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"AU"`) {
		t.Fatalf("response does not contain AU: %s", recorder.Body.String())
	}
}

func TestScriptEndpoint(t *testing.T) {
	query := url.Values{
		"name":    {"countries-blocked"},
		"country": {"AU"},
		"replace": {"true"},
	}
	request := httptest.NewRequest(http.MethodGet, "/api/script?"+query.Encode(), nil)
	recorder := httptest.NewRecorder()
	testHTTPHandler(t).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if disposition := recorder.Header().Get("Content-Disposition"); disposition != `attachment; filename="countries-blocked.rsc"` {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	if !strings.Contains(recorder.Body.String(), `comment="AU - Australia"`) {
		t.Fatalf("script does not contain country comment: %s", recorder.Body.String())
	}
}

func TestScriptEndpointRejectsInvalidName(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/script?name=bad%22name&country=AU", nil)
	recorder := httptest.NewRecorder()
	testHTTPHandler(t).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestScriptEndpointRejectsInvalidCountryCode(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/script?name=countries&country=AU%22%3B/system/reboot", nil)
	recorder := httptest.NewRecorder()
	testHTTPHandler(t).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "/system/reboot") {
		t.Fatalf("response reflects the invalid country code: %s", recorder.Body.String())
	}
}

func TestOversizedRequestTargetIsRejected(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	request.RequestURI = "/?" + strings.Repeat("a", maxRequestTargetBytes+1)
	recorder := httptest.NewRecorder()
	testHTTPHandler(t).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestURITooLong {
		t.Fatalf("status = %d, want 414", recorder.Code)
	}
}

func TestScriptEndpointRejectsWhenGeneratorCapacityIsFull(t *testing.T) {
	source := fakeDatabaseSource{
		catalog: testCatalog(t),
		status:  dbStatus{State: "ready", HasData: true},
	}
	app := &applicationHandler{
		source:      source,
		logger:      log.New(io.Discard, "", 0),
		scriptSlots: make(chan struct{}, 1),
	}
	app.scriptSlots <- struct{}{}

	request := httptest.NewRequest(http.MethodGet, "/api/script?name=countries&country=AU", nil)
	recorder := httptest.NewRecorder()
	app.script(recorder, request)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", recorder.Header().Get("Retry-After"))
	}
}

func TestSecurityHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	testHTTPHandler(t).ServeHTTP(recorder, request)

	headers := map[string]string{
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), geolocation=(), microphone=(), payment=(), usb=()",
		"Referrer-Policy":              "no-referrer",
		"Strict-Transport-Security":    "max-age=31536000",
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
	}
	for name, want := range headers {
		if got := recorder.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if policy := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "object-src 'none'") {
		t.Errorf("Content-Security-Policy does not block plugins: %q", policy)
	}
}

func TestPostIsRejected(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/script", nil)
	recorder := httptest.NewRecorder()
	testHTTPHandler(t).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}

func TestIndexEndpoint(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	testHTTPHandler(t).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "Country address lists") {
		t.Fatal("index page was not served")
	}
	if strings.Contains(recorder.Body.String(), "country-state") ||
		strings.Contains(recorder.Body.String(), "Loading country ranges") {
		t.Fatal("index page still contains the obsolete country loading placeholder")
	}
	if !strings.Contains(recorder.Body.String(), `id="country-grid"`) {
		t.Fatal("index page no longer contains the country grid")
	}
}
