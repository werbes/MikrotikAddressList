package main

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultDownloadEndpoint = "https://www.ip2location.com/download"
	maxArchiveSize          = 256 << 20
	maxLogPreviewBytes      = 512
	maxLogPreviewReadBytes  = 64 << 10
	maxRetryDelay           = time.Hour
	maxDownloadRedirects    = 10
)

type dbStatus struct {
	State            string     `json:"state"`
	Message          string     `json:"message"`
	HasData          bool       `json:"has_data"`
	Warning          bool       `json:"warning"`
	DatabaseVersion  string     `json:"database_version,omitempty"`
	FileSize         int64      `json:"file_size,omitempty"`
	RangeCount       int        `json:"range_count,omitempty"`
	CountryCount     int        `json:"country_count,omitempty"`
	LastCheckedAt    *time.Time `json:"last_checked_at,omitempty"`
	LastDownloadedAt *time.Time `json:"last_downloaded_at,omitempty"`
}

type databaseManager struct {
	config   appConfig
	endpoint string
	client   *http.Client
	logger   *log.Logger

	catalog atomic.Pointer[Catalog]
	mu      sync.RWMutex
	status  dbStatus

	refreshMu sync.Mutex
}

func newDatabaseManager(config appConfig, endpoint string, logger *log.Logger) *databaseManager {
	if endpoint == "" {
		endpoint = defaultDownloadEndpoint
	}
	return &databaseManager{
		config:   config,
		endpoint: endpoint,
		client:   newDownloadClient(config.DownloadTimeout),
		logger:   logger,
		status: dbStatus{
			State:   "loading",
			Message: "Loading the local IP2Location database.",
		},
	}
}

func newDownloadClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) == 0 {
				return errors.New("download endpoint redirect has no request history")
			}
			if len(via) >= maxDownloadRedirects {
				return errors.New("download endpoint redirected too many times")
			}

			initial := via[0].URL
			if strings.EqualFold(initial.Scheme, "https") && !strings.EqualFold(request.URL.Scheme, "https") {
				return errors.New("download endpoint attempted an HTTPS downgrade")
			}

			if !sameOrigin(initial, request.URL) {
				query := request.URL.Query()
				if query.Has("token") {
					query.Del("token")
					request.URL.RawQuery = query.Encode()
				}
			}
			return nil
		},
	}
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Host, right.Host)
}

func (m *databaseManager) Snapshot() *Catalog {
	return m.catalog.Load()
}

func (m *databaseManager) Status() dbStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *databaseManager) LoadExisting() {
	path := m.databasePath()
	m.recoverDatabaseSwap()

	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		m.setStatus(dbStatus{
			State:   "downloading",
			Message: "The IP2Location database has not been downloaded yet.",
		})
		return
	}
	if err != nil {
		m.setStatus(dbStatus{
			State:   "error",
			Message: "The local database could not be inspected. Check the server logs.",
		})
		m.logger.Printf("database inspection failed: %v", err)
		return
	}

	m.setStatus(dbStatus{
		State:   "loading",
		Message: "Reading the local IP2Location database.",
	})
	catalog, err := loadCatalog(path)
	if err != nil {
		m.setStatus(dbStatus{
			State:   "error",
			Message: "The local database could not be loaded. Check the server logs.",
		})
		m.logger.Printf("database load failed: %v", err)
		return
	}

	downloadedAt := info.ModTime().UTC()
	m.catalog.Store(catalog)
	m.setStatus(dbStatus{
		State:            "ready",
		Message:          "Database loaded and ready.",
		HasData:          true,
		DatabaseVersion:  catalog.DatabaseDate,
		FileSize:         catalog.SourceBytes,
		RangeCount:       catalog.TotalRanges,
		CountryCount:     len(catalog.Countries),
		LastDownloadedAt: &downloadedAt,
	})
	m.logger.Printf(
		"loaded IP2Location %s: %d countries, %d merged ranges",
		catalog.DatabaseDate,
		len(catalog.Countries),
		catalog.TotalRanges,
	)
}

func (m *databaseManager) Run(ctx context.Context) {
	for {
		if err := m.refreshIfDue(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			m.logger.Printf("database refresh failed: %v", err)
		}

		delay := m.nextCheckDelay()
		m.logger.Printf("next database refresh check in %s", delay.Round(time.Second))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (m *databaseManager) refreshIfDue(ctx context.Context) error {
	if m.Snapshot() != nil {
		info, err := os.Stat(m.databasePath())
		if err == nil && time.Since(info.ModTime()) < m.config.RefreshInterval {
			return nil
		}
	}
	return m.refresh(ctx)
}

func (m *databaseManager) nextCheckDelay() time.Duration {
	if m.Snapshot() != nil {
		info, err := os.Stat(m.databasePath())
		if err == nil {
			remaining := m.config.RefreshInterval - time.Since(info.ModTime())
			if remaining > 0 {
				return remaining
			}
		}
	}
	return maxRetryDelay
}

func (m *databaseManager) refresh(ctx context.Context) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()

	now := time.Now().UTC()
	hasData := m.Snapshot() != nil
	if strings.TrimSpace(m.config.Token) == "" {
		err := errors.New("IP2LOCATION_TOKEN is not configured")
		m.setRefreshFailure(hasData, now)
		return err
	}

	state := "downloading"
	message := "Downloading the latest IP2Location database."
	if hasData {
		state = "updating"
		message = "Refreshing the IP2Location database in the background."
	}
	status := m.Status()
	status.State = state
	status.Message = message
	status.Warning = false
	status.LastCheckedAt = &now
	m.setStatus(status)

	binPath, err := m.downloadDatabase(ctx)
	if err != nil {
		m.setRefreshFailure(hasData, now)
		return err
	}
	defer os.Remove(binPath)

	catalog, err := loadCatalog(binPath)
	if err != nil {
		err = fmt.Errorf("downloaded database is invalid: %w", err)
		m.setRefreshFailure(hasData, now)
		return err
	}

	if err := os.Chtimes(binPath, now, now); err != nil {
		err = fmt.Errorf("could not timestamp the downloaded database: %w", err)
		m.setRefreshFailure(hasData, now)
		return err
	}
	if err := replaceFile(binPath, m.databasePath()); err != nil {
		err = fmt.Errorf("could not install the downloaded database: %w", err)
		m.setRefreshFailure(hasData, now)
		return err
	}

	m.catalog.Store(catalog)
	m.setStatus(dbStatus{
		State:            "ready",
		Message:          "Database updated and ready.",
		HasData:          true,
		DatabaseVersion:  catalog.DatabaseDate,
		FileSize:         catalog.SourceBytes,
		RangeCount:       catalog.TotalRanges,
		CountryCount:     len(catalog.Countries),
		LastCheckedAt:    &now,
		LastDownloadedAt: &now,
	})
	m.logger.Printf(
		"installed IP2Location %s: %d countries, %d merged ranges",
		catalog.DatabaseDate,
		len(catalog.Countries),
		catalog.TotalRanges,
	)
	return nil
}

func (m *databaseManager) setRefreshFailure(hasData bool, checkedAt time.Time) {
	status := m.Status()
	status.LastCheckedAt = &checkedAt
	status.Warning = true
	if hasData {
		status.State = "ready"
		status.HasData = true
		status.Message = "Database refresh failed; the existing data is still available."
	} else {
		status.State = "error"
		status.Message = "Database refresh failed. Check the server configuration and network access."
	}
	m.setStatus(status)
}

func (m *databaseManager) setStatus(status dbStatus) {
	m.mu.Lock()
	m.status = status
	m.mu.Unlock()
}

func (m *databaseManager) databasePath() string {
	return filepath.Join(m.config.DataDirectory, "ip2location.bin")
}

func (m *databaseManager) downloadDatabase(ctx context.Context) (string, error) {
	if err := os.MkdirAll(m.config.DataDirectory, 0o750); err != nil {
		return "", fmt.Errorf("could not create data directory: %w", err)
	}

	endpoint, err := url.Parse(m.endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid download endpoint: %w", err)
	}
	query := endpoint.Query()
	query.Set("token", m.config.Token)
	query.Set("file", m.config.DatabaseCode)
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "MikrotikAddressList/1.0")
	m.logger.Printf(
		"requesting database download: endpoint=%s database=%s timeout=%s",
		safeURLForLog(endpoint.String()),
		m.config.DatabaseCode,
		m.config.DownloadTimeout,
	)
	requestStarted := time.Now()
	response, err := m.client.Do(request)
	if err != nil {
		detail := m.describeDownloadRequestError(err)
		m.logger.Printf(
			"database download request failed after %s: endpoint=%s database=%s error=%s",
			time.Since(requestStarted).Round(time.Millisecond),
			safeURLForLog(endpoint.String()),
			m.config.DatabaseCode,
			detail,
		)
		return "", fmt.Errorf("download request failed: %s", detail)
	}
	defer response.Body.Close()

	contentType := redactDownloadToken(response.Header.Get("Content-Type"), m.config.Token)
	finalEndpoint := safeURLForLog(endpoint.String())
	if response.Request != nil && response.Request.URL != nil {
		finalEndpoint = safeURLForLog(response.Request.URL.String())
	}
	m.logger.Printf(
		"database download response received after %s: status=%d %s content_type=%q content_length=%d final_endpoint=%s",
		time.Since(requestStarted).Round(time.Millisecond),
		response.StatusCode,
		http.StatusText(response.StatusCode),
		contentType,
		response.ContentLength,
		finalEndpoint,
	)

	if response.StatusCode != http.StatusOK {
		preview := responsePreview(response.Body, m.config.Token)
		if preview != "" {
			m.logger.Printf(
				"database download rejected: status=%d %s response_preview=%q",
				response.StatusCode,
				http.StatusText(response.StatusCode),
				preview,
			)
			return "", fmt.Errorf(
				"download endpoint returned HTTP %d %s: %q",
				response.StatusCode,
				http.StatusText(response.StatusCode),
				preview,
			)
		}
		return "", fmt.Errorf(
			"download endpoint returned HTTP %d %s",
			response.StatusCode,
			http.StatusText(response.StatusCode),
		)
	}
	if response.ContentLength > maxArchiveSize {
		return "", fmt.Errorf("download archive exceeds the %d byte limit", maxArchiveSize)
	}

	zipFile, err := os.CreateTemp(m.config.DataDirectory, ".ip2location-*.zip.part")
	if err != nil {
		return "", err
	}
	zipPath := zipFile.Name()
	defer os.Remove(zipPath)

	written, copyErr := io.Copy(zipFile, io.LimitReader(response.Body, maxArchiveSize+1))
	syncErr := zipFile.Sync()
	closeErr := zipFile.Close()
	if copyErr != nil {
		return "", fmt.Errorf("could not save download archive: %w", copyErr)
	}
	if written > maxArchiveSize {
		return "", fmt.Errorf("download archive exceeds the %d byte limit", maxArchiveSize)
	}
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	m.logger.Printf(
		"database archive downloaded: bytes=%d elapsed=%s",
		written,
		time.Since(requestStarted).Round(time.Millisecond),
	)

	archive, err := zip.OpenReader(zipPath)
	if err != nil {
		preview := filePreview(zipPath, m.config.Token)
		if preview != "" {
			m.logger.Printf(
				"database download is not a valid ZIP archive: content_type=%q bytes=%d response_preview=%q",
				contentType,
				written,
				preview,
			)
			return "", fmt.Errorf(
				"download is not a valid ZIP archive: %w; response begins %q",
				err,
				preview,
			)
		}
		return "", fmt.Errorf("download is not a valid ZIP archive: %w", err)
	}
	defer archive.Close()

	var databaseFile *zip.File
	for _, file := range archive.File {
		if !file.FileInfo().IsDir() && strings.EqualFold(filepath.Ext(file.Name), ".bin") {
			databaseFile = file
			break
		}
	}
	if databaseFile == nil {
		return "", errors.New("download archive does not contain a BIN database")
	}
	if databaseFile.UncompressedSize64 > maxDatabaseSize {
		return "", fmt.Errorf("uncompressed database exceeds the %d byte limit", maxDatabaseSize)
	}

	reader, err := databaseFile.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()

	binFile, err := os.CreateTemp(m.config.DataDirectory, ".ip2location-*.bin.part")
	if err != nil {
		return "", err
	}
	binPath := binFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			if err := os.Remove(binPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				m.logger.Printf("could not remove temporary database: %v", err)
			}
		}
	}()

	extracted, copyErr := io.Copy(binFile, io.LimitReader(reader, maxDatabaseSize+1))
	syncErr = binFile.Sync()
	closeErr = binFile.Close()
	if copyErr != nil {
		return "", fmt.Errorf("could not extract database: %w", copyErr)
	}
	if extracted > maxDatabaseSize {
		return "", fmt.Errorf("uncompressed database exceeds the %d byte limit", maxDatabaseSize)
	}
	if extracted < ip2LocationHeaderSize {
		return "", errors.New("extracted database is too small")
	}
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Chmod(binPath, 0o600); err != nil {
		return "", err
	}

	cleanup = false
	return binPath, nil
}

func (m *databaseManager) describeDownloadRequestError(err error) string {
	var requestErr *url.Error
	if errors.As(err, &requestErr) {
		endpoint := safeURLForLog(requestErr.URL)
		reason := redactDownloadToken(requestErr.Err.Error(), m.config.Token)
		if requestErr.Timeout() {
			return fmt.Sprintf("%s request to %s timed out: %s", requestErr.Op, endpoint, reason)
		}
		return fmt.Sprintf("%s %s: %s", requestErr.Op, endpoint, reason)
	}
	return redactDownloadToken(err.Error(), m.config.Token)
}

func safeURLForLog(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<invalid endpoint>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

func redactDownloadToken(value, token string) string {
	if token == "" {
		return value
	}
	for _, secret := range []string{token, url.QueryEscape(token), url.PathEscape(token)} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func responsePreview(reader io.Reader, token string) string {
	readLimit := int64(maxLogPreviewReadBytes + 3*len(token) + 4)
	data, _ := io.ReadAll(io.LimitReader(reader, readLimit))
	preview := redactDownloadToken(strings.ToValidUTF8(string(data), "\uFFFD"), token)
	preview = strings.Join(strings.Fields(preview), " ")

	runes := []rune(preview)
	if len(runes) > maxLogPreviewBytes {
		return string(runes[:maxLogPreviewBytes]) + "..."
	}
	return preview
}

func filePreview(path, token string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	return responsePreview(file, token)
}

func replaceFile(source, destination string) error {
	backup := destination + ".previous"
	_ = os.Remove(backup)

	hadDestination := false
	if _, err := os.Stat(destination); err == nil {
		hadDestination = true
		if err := os.Rename(destination, backup); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.Rename(source, destination); err != nil {
		if hadDestination {
			_ = os.Rename(backup, destination)
		}
		return err
	}
	if hadDestination {
		_ = os.Remove(backup)
	}
	return nil
}

func (m *databaseManager) recoverDatabaseSwap() {
	destination := m.databasePath()
	backup := destination + ".previous"
	if _, err := os.Stat(destination); err == nil {
		return
	}
	if _, err := os.Stat(backup); err != nil {
		return
	}
	if err := os.Rename(backup, destination); err != nil {
		m.logger.Printf("could not restore previous database: %v", err)
	}
}
