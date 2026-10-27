package main

import "testing"

func TestTLSFilesMustBeConfiguredTogether(t *testing.T) {
	t.Setenv("TLS_CERT_FILE", "server.crt")
	t.Setenv("TLS_KEY_FILE", "")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() error = nil with only a TLS certificate configured")
	}
}
