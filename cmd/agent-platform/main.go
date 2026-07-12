package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"agent-platform/internal/app"
	"agent-platform/internal/builtins"
	"agent-platform/internal/config"
)

const gracefulShutdownTimeout = 3 * time.Second

type shutdownServer interface {
	Shutdown(context.Context) error
	Close() error
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := runHealthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	startedAt := time.Now()
	log.Printf("starting runtime: pid=%d", os.Getpid())
	if binDir, err := builtins.ConfigureProcessPath(); err != nil {
		log.Printf("configure builtin PATH: %v", err)
	} else if binDir != "" {
		log.Printf("builtin PATH enabled: %s", binDir)
	}

	configOptions, err := parseConfigOptions(os.Args[1:])
	if err != nil {
		log.Fatalf("startup failed: %v", err)
	}

	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	appInitStartedAt := time.Now()
	application, err := app.New(rootCtx, configOptions)
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

func runHealthcheck() error {
	port := strings.TrimSpace(os.Getenv("SERVER_PORT"))
	if port == "" {
		port = "8080"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("runtime health probe failed: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("runtime health probe returned %s", response.Status)
	}
	return nil
}

func parseConfigOptions(args []string) (config.LoadOptions, error) {
	fs := flag.NewFlagSet("agent-platform", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	options := config.LoadOptions{}
	fs.StringVar(&options.ConfigDir, "config-dir", "", "configuration root containing .env and configs/")
	fs.StringVar(&options.Port, "port", "", "server listen port")
	if err := fs.Parse(args); err != nil {
		return config.LoadOptions{}, err
	}
	if remaining := fs.Args(); len(remaining) > 0 {
		return config.LoadOptions{}, fmt.Errorf("unexpected argument(s): %s", strings.Join(remaining, " "))
	}
	return options, nil
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
