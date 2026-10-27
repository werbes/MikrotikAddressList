package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	logger := log.New(os.Stdout, "", log.Ldate|log.Ltime|log.LUTC)
	if err := run(logger); err != nil {
		logger.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run(logger *log.Logger) error {
	config, err := loadConfig()
	if err != nil {
		return err
	}

	manager := newDatabaseManager(config, defaultDownloadEndpoint, logger)
	manager.LoadExisting()

	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go manager.Run(rootContext)

	server := &http.Server{
		Addr:              config.ListenAddress,
		Handler:           newHTTPHandler(manager, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          logger,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Printf("listening on %s", config.ListenAddress)
		if config.TLSCertificate != "" {
			serverErrors <- server.ListenAndServeTLS(config.TLSCertificate, config.TLSKey)
			return
		}
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-rootContext.Done():
		logger.Print("shutdown requested")
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		return err
	}
	return nil
}
