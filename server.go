package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxRequestTargetBytes       = 16 << 10
	maxConcurrentScriptRequests = 8
)

//go:embed web/*
var embeddedWeb embed.FS

type databaseSource interface {
	Snapshot() *Catalog
	Status() dbStatus
}

type applicationHandler struct {
	source      databaseSource
	logger      *log.Logger
	scriptSlots chan struct{}
}

func newHTTPHandler(source databaseSource, logger *log.Logger) http.Handler {
	return newApplicationHandler(source, logger)
}

func newApplicationHandler(source databaseSource, logger *log.Logger) http.Handler {
	app := &applicationHandler{
		source:      source,
		logger:      logger,
		scriptSlots: make(chan struct{}, maxConcurrentScriptRequests),
	}
	mux := http.NewServeMux()

	webRoot, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(webRoot))
	mux.Handle("GET /static/", http.StripPrefix("/static/", fileServer))
	mux.HandleFunc("GET /{$}", app.index)
	mux.HandleFunc("GET /api/countries", app.countries)
	mux.HandleFunc("GET /api/status", app.status)
	mux.HandleFunc("GET /api/script", app.script)
	mux.HandleFunc("GET /healthz", app.health)

	handler := limitRequestTarget(mux)
	return securityHeaders(accessLog(logger, recoverPanics(logger, handler)))
}

func (a *applicationHandler) index(writer http.ResponseWriter, request *http.Request) {
	http.ServeFileFS(writer, request, embeddedWeb, "web/index.html")
}

func (a *applicationHandler) countries(writer http.ResponseWriter, _ *http.Request) {
	catalog := a.source.Snapshot()
	if catalog == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{
			"error":    "The IP2Location database is not available yet.",
			"database": a.source.Status(),
		})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"countries": catalog.Countries,
		"database":  a.source.Status(),
	})
}

func (a *applicationHandler) status(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, a.source.Status())
}

func (a *applicationHandler) script(writer http.ResponseWriter, request *http.Request) {
	catalog := a.source.Snapshot()
	if catalog == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{
			"error": "The IP2Location database is not available yet.",
		})
		return
	}

	query := request.URL.Query()
	listName := strings.TrimSpace(query.Get("name"))
	if err := validateListName(listName); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	var countryCodes []string
	for _, value := range query["country"] {
		for _, code := range strings.Split(value, ",") {
			countryCodes = append(countryCodes, code)
		}
	}
	selected, err := selectCountries(catalog, countryCodes)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	replaceExisting := true
	if value := query.Get("replace"); value != "" {
		replaceExisting, err = strconv.ParseBool(value)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{
				"error": "replace must be true or false",
			})
			return
		}
	}

	select {
	case a.scriptSlots <- struct{}{}:
		defer func() { <-a.scriptSlots }()
	default:
		writer.Header().Set("Retry-After", "1")
		writeJSON(writer, http.StatusTooManyRequests, map[string]string{
			"error": "The service is busy generating other scripts. Please retry shortly.",
		})
		return
	}

	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.rsc"`, listName))
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")

	buffered := bufio.NewWriterSize(writer, 64<<10)
	err = generateMikrotikScript(buffered, catalog, scriptOptions{
		ListName:        listName,
		CountryCodes:    countryCodes,
		ReplaceExisting: replaceExisting,
	}, selected, time.Now())
	if err == nil {
		err = buffered.Flush()
	}
	if err != nil {
		a.logger.Printf("script generation ended early: %v", err)
	}
}

func (a *applicationHandler) health(writer http.ResponseWriter, _ *http.Request) {
	status := a.source.Status()
	if a.source.Snapshot() == nil {
		writeJSON(writer, http.StatusServiceUnavailable, status)
		return
	}
	writeJSON(writer, http.StatusOK, status)
}

func writeJSON(writer http.ResponseWriter, statusCode int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set(
			"Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; require-trusted-types-for 'script'; trusted-types 'none'",
		)
		writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		writer.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Strict-Transport-Security", "max-age=31536000")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(writer, request)
	})
}

func limitRequestTarget(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if len(request.RequestURI) > maxRequestTargetBytes {
			http.Error(writer, "Request target is too long", http.StatusRequestURITooLong)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func recoverPanics(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Printf("panic serving %q: %v", request.URL.Path, recovered)
				http.Error(writer, "Internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(writer, request)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(statusCode int) {
	w.status = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func accessLog(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		wrapped := &statusWriter{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(wrapped, request)
		logger.Printf("%s %q %d %s", request.Method, request.URL.Path, wrapped.status, time.Since(started).Round(time.Millisecond))
	})
}
