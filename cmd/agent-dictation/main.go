package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agent-platform/internal/dictation"
	"agent-platform/internal/httpclient"
)

func main() {
	path := flag.String("config", "configs/dictation.yml", "independent dictation configuration")
	flag.Parse()
	cfg, err := dictation.LoadConfig(*path)
	if err != nil {
		log.Fatal(err)
	}
	if err = httpclient.ConfigureDefault(cfg.HTTPProxy); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	handler, err := dictation.NewServer(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Print(err)
		}
	case <-ctx.Done():
	}
	handler.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		os.Exit(1)
	}
}
