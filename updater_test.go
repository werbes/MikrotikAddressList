package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testZIP(t *testing.T, bin []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("IP2LOCATION-LITE-DB3.BIN")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(bin); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestDatabaseManagerRefresh(t *testing.T) {
	bin := testDatabase(t)
	archive := testZIP(t, bin)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("token") != "test-token" {
			http.Error(writer, "bad token", http.StatusForbidden)
			return
		}
		if query.Get("file") != "DB3LITEBIN" {
			http.Error(writer, "bad product", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/zip")
		_, _ = writer.Write(archive)
	}))
	defer server.Close()

	directory := t.TempDir()
	manager := newDatabaseManager(appConfig{
		DataDirectory:   directory,
		DatabaseCode:    "DB3LITEBIN",
		Token:           "test-token",
		RefreshInterval: 24 * time.Hour,
		DownloadTimeout: 5 * time.Second,
	}, server.URL, log.New(io.Discard, "", 0))

	if err := manager.refresh(context.Background()); err != nil {
		t.Fatalf("refresh() error = %v", err)
	}
	if manager.Snapshot() == nil {
		t.Fatal("Snapshot() = nil after refresh")
	}
	if manager.Status().State != "ready" {
		t.Fatalf("status state = %q, want ready", manager.Status().State)
	}

	installed, err := os.ReadFile(filepath.Join(directory, "ip2location.bin"))
	if err != nil {
		t.Fatalf("installed database could not be read: %v", err)
	}
	if !bytes.Equal(installed, bin) {
		t.Fatal("installed database differs from downloaded database")
	}
	if matches, err := filepath.Glob(filepath.Join(directory, ".ip2location-*.part")); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Fatalf("temporary files were not cleaned up: %v", matches)
	}
}

func TestDatabaseManagerSkipsFreshLocalDatabase(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "ip2location.bin")
	if err := os.WriteFile(path, testDatabase(t), 0o640); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}

	manager := newDatabaseManager(appConfig{
		DataDirectory:   directory,
		Token:           "",
		RefreshInterval: 24 * time.Hour,
		DownloadTimeout: time.Second,
	}, "", log.New(io.Discard, "", 0))
	manager.LoadExisting()

	if err := manager.refreshIfDue(context.Background()); err != nil {
		t.Fatalf("refreshIfDue() error = %v; a fresh database should not need a token", err)
	}
	if manager.Status().Warning {
		t.Fatal("fresh local database was unexpectedly refreshed")
	}
}

func TestDatabaseManagerKeepsDataWhenTokenIsMissing(t *testing.T) {
	manager := newDatabaseManager(appConfig{
		DataDirectory:   t.TempDir(),
		Token:           "",
		RefreshInterval: 24 * time.Hour,
		DownloadTimeout: time.Second,
	}, "", log.New(io.Discard, "", 0))
	manager.catalog.Store(testCatalog(t))

	err := manager.refresh(context.Background())
	if err == nil {
		t.Fatal("refresh() error = nil")
	}
	if manager.Snapshot() == nil {
		t.Fatal("existing catalog was discarded after refresh failure")
	}
	if !manager.Status().Warning {
		t.Fatal("status warning = false, want true")
	}
}

func TestDatabaseManagerDoesNotExposeTokenInErrors(t *testing.T) {
	const token = "secret-download-token"
	var logs bytes.Buffer
	manager := newDatabaseManager(appConfig{
		DataDirectory:   t.TempDir(),
		DatabaseCode:    "DB3LITEBIN",
		Token:           token,
		RefreshInterval: 24 * time.Hour,
		DownloadTimeout: time.Second,
	}, "https://downloads.example.test/archive", log.New(&logs, "", 0))
	manager.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("proxy handshake failed")
	})

	err := manager.refresh(context.Background())
	if err == nil {
		t.Fatal("refresh() error = nil")
	}
	if !strings.Contains(err.Error(), "proxy handshake failed") {
		t.Fatalf("refresh error = %q, want underlying network error", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("refresh error exposes token: %v", err)
	}
	if strings.Contains(manager.Status().Message, token) {
		t.Fatalf("database status exposes token: %s", manager.Status().Message)
	}
	if !strings.Contains(logs.String(), "database download request failed") ||
		!strings.Contains(logs.String(), "proxy handshake failed") {
		t.Fatalf("database log does not include the network failure: %s", logs.String())
	}
	if strings.Contains(logs.String(), token) {
		t.Fatalf("database log exposes token: %s", logs.String())
	}
}

func TestDatabaseManagerLogsHTTPFailureResponseWithoutToken(t *testing.T) {
	const token = "secret-download-token"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(writer, "INVALID TOKEN: %s\n", token)
	}))
	defer server.Close()

	var logs bytes.Buffer
	manager := newDatabaseManager(appConfig{
		DataDirectory:   t.TempDir(),
		DatabaseCode:    "DB3LITEBIN",
		Token:           token,
		RefreshInterval: 24 * time.Hour,
		DownloadTimeout: 5 * time.Second,
	}, server.URL, log.New(&logs, "", 0))

	err := manager.refresh(context.Background())
	if err == nil {
		t.Fatal("refresh() error = nil")
	}
	if !strings.Contains(err.Error(), "HTTP 403 Forbidden") ||
		!strings.Contains(err.Error(), "INVALID TOKEN") ||
		!strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("refresh error does not describe the server response: %v", err)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(logs.String(), token) {
		t.Fatalf("HTTP failure exposed the token: error=%q logs=%q", err, logs.String())
	}
	if !strings.Contains(logs.String(), "database download rejected") ||
		!strings.Contains(logs.String(), "response_preview") {
		t.Fatalf("database log does not include the rejected response: %s", logs.String())
	}
}

func TestDatabaseManagerLogsInvalidZIPResponseWithoutToken(t *testing.T) {
	const token = "secret-download-token"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintf(writer, "INVALID TOKEN: %s\n", token)
	}))
	defer server.Close()

	var logs bytes.Buffer
	manager := newDatabaseManager(appConfig{
		DataDirectory:   t.TempDir(),
		DatabaseCode:    "DB3LITEBIN",
		Token:           token,
		RefreshInterval: 24 * time.Hour,
		DownloadTimeout: 5 * time.Second,
	}, server.URL, log.New(&logs, "", 0))

	err := manager.refresh(context.Background())
	if err == nil {
		t.Fatal("refresh() error = nil")
	}
	if !strings.Contains(err.Error(), "not a valid ZIP archive") ||
		!strings.Contains(err.Error(), "INVALID TOKEN") ||
		!strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("refresh error does not describe the invalid archive: %v", err)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(logs.String(), token) {
		t.Fatalf("invalid archive exposed the token: error=%q logs=%q", err, logs.String())
	}
	if !strings.Contains(logs.String(), "database download is not a valid ZIP archive") ||
		!strings.Contains(logs.String(), "response_preview") {
		t.Fatalf("database log does not include the invalid ZIP response: %s", logs.String())
	}
}

func TestDatabaseManagerFollowsCrossOriginRedirect(t *testing.T) {
	const token = "secret-download-token"
	bin := testDatabase(t)
	archive := testZIP(t, bin)
	var redirectedRequests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectedRequests.Add(1)
		if request.URL.Query().Get("token") != "" {
			t.Error("download token was forwarded to the cross-origin redirect target")
		}
		if request.URL.Query().Get("X-Amz-Signature") != "test-signature" {
			t.Error("signed redirect query parameters were not preserved")
		}
		_, _ = writer.Write(archive)
	}))
	defer destination.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		target := destination.URL + "/archive.zip?X-Amz-Signature=test-signature&token=" + url.QueryEscape(token)
		http.Redirect(writer, request, target, http.StatusFound)
	}))
	defer origin.Close()

	manager := newDatabaseManager(appConfig{
		DataDirectory:   t.TempDir(),
		DatabaseCode:    "DB3LITEBIN",
		Token:           token,
		RefreshInterval: 24 * time.Hour,
		DownloadTimeout: 5 * time.Second,
	}, origin.URL, log.New(io.Discard, "", 0))

	if err := manager.refresh(context.Background()); err != nil {
		t.Fatalf("refresh() error = %v", err)
	}
	if redirectedRequests.Load() != 1 {
		t.Fatalf("cross-origin redirect requests = %d, want 1", redirectedRequests.Load())
	}
	if manager.Snapshot() == nil {
		t.Fatal("Snapshot() = nil after cross-origin redirect")
	}
}

func TestDownloadClientRejectsHTTPSDowngrade(t *testing.T) {
	client := newDownloadClient(time.Second)
	initial, err := http.NewRequest(http.MethodGet, "https://www.ip2location.com/download", nil)
	if err != nil {
		t.Fatal(err)
	}
	redirected, err := http.NewRequest(http.MethodGet, "http://redirect.example/archive.zip", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := client.CheckRedirect(redirected, []*http.Request{initial}); err == nil {
		t.Fatal("CheckRedirect() error = nil for an HTTPS downgrade")
	}
}

func TestDownloadClientCapsRedirects(t *testing.T) {
	client := newDownloadClient(time.Second)
	history := make([]*http.Request, maxDownloadRedirects)
	for index := range history {
		history[index], _ = http.NewRequest(http.MethodGet, "https://www.ip2location.com/download", nil)
	}
	redirected, _ := http.NewRequest(http.MethodGet, "https://storage.example/archive.zip", nil)

	if err := client.CheckRedirect(redirected, history); err == nil {
		t.Fatal("CheckRedirect() error = nil after the redirect limit")
	}
}

func TestDatabaseManagerDoesNotLogTokenReflectedInHTTPStatus(t *testing.T) {
	const token = "secret-download-token"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			http.Error(writer, "hijacking is unavailable", http.StatusInternalServerError)
			return
		}
		connection, buffered, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = fmt.Fprintf(buffered, "HTTP/1.1 403 %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", token)
		_ = buffered.Flush()
	}))
	defer server.Close()

	manager := newDatabaseManager(appConfig{
		DataDirectory:   t.TempDir(),
		DatabaseCode:    "DB3LITEBIN",
		Token:           token,
		RefreshInterval: 24 * time.Hour,
		DownloadTimeout: 5 * time.Second,
	}, server.URL, log.New(io.Discard, "", 0))

	err := manager.refresh(context.Background())
	if err == nil {
		t.Fatal("refresh() error = nil")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(manager.Status().Message, token) {
		t.Fatalf("HTTP status reflected the token into an error: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("error = %q, want numeric HTTP status", err)
	}
}

func TestDatabaseManagerAllowsSameOriginRedirect(t *testing.T) {
	bin := testDatabase(t)
	archive := testZIP(t, bin)
	var archiveRequested atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/download" {
			if request.URL.Query().Get("token") != "test-token" {
				http.Error(writer, "token was lost during redirect", http.StatusForbidden)
				return
			}
			http.Redirect(writer, request, "/archive", http.StatusFound)
			return
		}
		if request.URL.Path != "/archive" {
			http.NotFound(writer, request)
			return
		}
		archiveRequested.Store(true)
		_, _ = writer.Write(archive)
	}))
	defer server.Close()

	manager := newDatabaseManager(appConfig{
		DataDirectory:   t.TempDir(),
		DatabaseCode:    "DB3LITEBIN",
		Token:           "test-token",
		RefreshInterval: 24 * time.Hour,
		DownloadTimeout: 5 * time.Second,
	}, server.URL+"/download", log.New(io.Discard, "", 0))

	if err := manager.refresh(context.Background()); err != nil {
		t.Fatalf("refresh() error = %v", err)
	}
	if !archiveRequested.Load() {
		t.Fatal("same-origin redirect did not reach the archive")
	}
}

func TestDatabaseLoadErrorDoesNotExposeFilesystemPaths(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "ip2location.bin"), []byte("invalid"), 0o640); err != nil {
		t.Fatal(err)
	}

	manager := newDatabaseManager(appConfig{
		DataDirectory:   directory,
		RefreshInterval: 24 * time.Hour,
		DownloadTimeout: time.Second,
	}, "", log.New(io.Discard, "", 0))
	manager.LoadExisting()

	status := manager.Status()
	if strings.Contains(status.Message, directory) {
		t.Fatalf("status exposed a filesystem path: %s", status.Message)
	}
	if status.State != "error" {
		t.Fatalf("state = %q, want error", status.State)
	}
}
