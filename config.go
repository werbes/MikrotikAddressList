package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultListenAddress  = ":8080"
	defaultDataDirectory  = "./data"
	defaultDatabaseCode   = "DB3LITEBIN"
	defaultRefresh        = 24 * time.Hour
	defaultDownloadTimout = 10 * time.Minute
)

type appConfig struct {
	ListenAddress   string
	DataDirectory   string
	DatabaseCode    string
	Token           string
	TLSCertificate  string
	TLSKey          string
	RefreshInterval time.Duration
	DownloadTimeout time.Duration
}

func loadConfig() (appConfig, error) {
	listenAddress := strings.TrimSpace(os.Getenv("LISTEN_ADDR"))
	if listenAddress == "" {
		port := strings.TrimSpace(os.Getenv("PORT"))
		if port == "" {
			listenAddress = defaultListenAddress
		} else {
			listenAddress = ":" + strings.TrimPrefix(port, ":")
		}
	}

	refreshInterval, err := durationFromEnvironment("IP2LOCATION_REFRESH_INTERVAL", defaultRefresh)
	if err != nil {
		return appConfig{}, err
	}
	if refreshInterval < time.Minute {
		return appConfig{}, fmt.Errorf("IP2LOCATION_REFRESH_INTERVAL must be at least 1m")
	}

	downloadTimeout, err := durationFromEnvironment("IP2LOCATION_DOWNLOAD_TIMEOUT", defaultDownloadTimout)
	if err != nil {
		return appConfig{}, err
	}
	if downloadTimeout < 30*time.Second {
		return appConfig{}, fmt.Errorf("IP2LOCATION_DOWNLOAD_TIMEOUT must be at least 30s")
	}

	tlsCertificate := strings.TrimSpace(os.Getenv("TLS_CERT_FILE"))
	tlsKey := strings.TrimSpace(os.Getenv("TLS_KEY_FILE"))
	if (tlsCertificate == "") != (tlsKey == "") {
		return appConfig{}, fmt.Errorf("TLS_CERT_FILE and TLS_KEY_FILE must be set together")
	}

	return appConfig{
		ListenAddress:   listenAddress,
		DataDirectory:   valueOrDefault("DATA_DIR", defaultDataDirectory),
		DatabaseCode:    valueOrDefault("IP2LOCATION_DATABASE_CODE", defaultDatabaseCode),
		Token:           strings.TrimSpace(os.Getenv("IP2LOCATION_TOKEN")),
		TLSCertificate:  tlsCertificate,
		TLSKey:          tlsKey,
		RefreshInterval: refreshInterval,
		DownloadTimeout: downloadTimeout,
	}, nil
}

func valueOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func durationFromEnvironment(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration such as 24h: %w", key, err)
	}
	return parsed, nil
}
