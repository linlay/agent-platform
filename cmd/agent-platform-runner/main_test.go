package main

import (
	"context"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

type stubShutdownServer struct {
	shutdownFn func(context.Context) error
	closeFn    func() error
}

func (s stubShutdownServer) Shutdown(ctx context.Context) error {
	if s.shutdownFn != nil {
		return s.shutdownFn(ctx)
	}
	return nil
}

func (s stubShutdownServer) Close() error {
	if s.closeFn != nil {
		return s.closeFn()
	}
	return nil
}

func TestShutdownHTTPServerForceClosesOnTimeout(t *testing.T) {
	var (
		closeCalls int
		mu         sync.Mutex
	)
	server := stubShutdownServer{
		shutdownFn: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		closeFn: func() error {
			mu.Lock()
			defer mu.Unlock()
			closeCalls++
			return nil
		},
	}

	err := shutdownHTTPServer(server, 20*time.Millisecond)
	if err == nil {
		t.Fatalf("expected shutdown error")
	}

	mu.Lock()
	gotCloseCalls := closeCalls
	mu.Unlock()
	if gotCloseCalls != 1 {
		t.Fatalf("expected force close to be called once, got %d", gotCloseCalls)
	}
}

func TestWaitForShutdownCancelsRootAndExitsOnSecondSignal(t *testing.T) {
	signals := make(chan os.Signal, 2)
	shutdownStarted := make(chan struct{}, 1)
	releaseShutdown := make(chan struct{})
	cancelCalled := make(chan struct{}, 1)
	exitCodes := make(chan int, 1)

	server := stubShutdownServer{
		shutdownFn: func(ctx context.Context) error {
			shutdownStarted <- struct{}{}
			<-releaseShutdown
			return nil
		},
	}

	cancelRoot := func() {
		cancelCalled <- struct{}{}
	}

	done := make(chan error, 1)
	go func() {
		done <- waitForShutdown(server, cancelRoot, signals, time.Second, func(code int) {
			exitCodes <- code
		})
	}()

	signals <- syscall.SIGTERM

	select {
	case <-cancelCalled:
	case <-time.After(time.Second):
		t.Fatalf("expected root context cancel to be called")
	}

	select {
	case <-shutdownStarted:
	case <-time.After(time.Second):
		t.Fatalf("expected shutdown to start")
	}

	signals <- os.Interrupt

	select {
	case code := <-exitCodes:
		if code != 1 {
			t.Fatalf("expected exit code 1, got %d", code)
		}
	case <-time.After(time.Second):
		t.Fatalf("expected second signal to force exit")
	}

	close(releaseShutdown)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForShutdown returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("waitForShutdown did not return")
	}
}

func TestWaitForShutdownDisarmsSecondSignalWatcherAfterNormalReturn(t *testing.T) {
	signals := make(chan os.Signal, 2)
	cancelCalled := make(chan struct{}, 1)
	exitCodes := make(chan int, 1)

	server := stubShutdownServer{
		shutdownFn: func(ctx context.Context) error {
			return nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- waitForShutdown(server, func() {
			cancelCalled <- struct{}{}
		}, signals, time.Second, func(code int) {
			exitCodes <- code
		})
	}()

	signals <- syscall.SIGTERM

	select {
	case <-cancelCalled:
	case <-time.After(time.Second):
		t.Fatalf("expected root context cancel to be called")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForShutdown returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("waitForShutdown did not return")
	}

	signals <- os.Interrupt

	select {
	case code := <-exitCodes:
		t.Fatalf("did not expect exit after normal return, got code %d", code)
	case <-time.After(100 * time.Millisecond):
	}
}
