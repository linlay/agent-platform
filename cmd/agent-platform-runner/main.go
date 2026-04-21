package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"agent-platform-runner-go/internal/app"
	"agent-platform-runner-go/internal/config"
)

const gracefulShutdownTimeout = 3 * time.Second

type shutdownServer interface {
	Shutdown(context.Context) error
	Close() error
}

func main() {
	startedAt := time.Now()
	log.Printf("starting runner: pid=%d", os.Getpid())

	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	appInitStartedAt := time.Now()
	application, err := app.New(rootCtx)
	if err != nil {
		log.Fatalf("startup failed during app init after %s: %v", startupElapsed(appInitStartedAt), err)
	}
	defer func() {
		if err := application.Close(); err != nil {
			log.Printf("app close: %v", err)
		}
	}()

	server := &http.Server{
		Addr:              application.Config.ServerAddress(),
		Handler:           application.Router,
		BaseContext:       func(net.Listener) context.Context { return rootCtx },
		ReadHeaderTimeout: 10 * time.Second,
	}
	runtimeDescription := resolvedRuntimeDescription(application.Config)
	log.Printf(
		"server ready: %s addr=%s app_init=%s total=%s",
		runtimeDescription,
		server.Addr,
		startupElapsed(appInitStartedAt),
		startupElapsed(startedAt),
	)

	go func() {
		listenStartedAt := time.Now()
		log.Printf("listening on %s (%s)", server.Addr, runtimeDescription)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen on %s failed after %s: %v", server.Addr, startupElapsed(listenStartedAt), err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	shutdownStartedAt := time.Now()
	if err := waitForShutdown(server, cancelRoot, stop, gracefulShutdownTimeout, os.Exit); err != nil {
		log.Printf("shutdown: %v", err)
	}
	log.Printf("shutdown complete in %s", startupElapsed(shutdownStartedAt))
}

func startupElapsed(startedAt time.Time) time.Duration {
	return time.Since(startedAt).Round(time.Millisecond)
}

func resolvedRuntimeDescription(cfg config.Config) string {
	if strings.HasPrefix(cfg.Paths.RegistriesDir, "/opt/") &&
		strings.HasPrefix(cfg.Paths.AgentsDir, "/opt/") &&
		strings.HasPrefix(cfg.Paths.ChatsDir, "/opt/") {
		return "mode=compose/container"
	}
	if hostPort, ok := os.LookupEnv("HOST_PORT"); ok && strings.TrimSpace(hostPort) != "" {
		return fmt.Sprintf("mode=local host_port=%s", strings.TrimSpace(hostPort))
	}
	return "mode=local"
}

func waitForShutdown(server shutdownServer, cancelRoot context.CancelFunc, signals <-chan os.Signal, timeout time.Duration, exit func(int)) error {
	sig := <-signals
	log.Printf("shutdown signal received: %s", sig)
	if cancelRoot != nil {
		cancelRoot()
	}
	if exit != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case secondSig := <-signals:
				log.Printf("second shutdown signal received: %s, forcing exit", secondSig)
				// This is an intentional hard exit that skips deferred cleanup.
				exit(1)
			case <-done:
			}
		}()
	}
	return shutdownHTTPServer(server, timeout)
}

func shutdownHTTPServer(server shutdownServer, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			return fmt.Errorf("graceful shutdown failed: %w (force close: %v)", err, closeErr)
		}
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}
	return nil
}
