package main

import "testing"

func TestParseConfigOptions(t *testing.T) {
	options, err := parseConfigOptions([]string{
		"--config-dir", "/tmp/config",
		"--runtime-dir", "/tmp/runtime",
		"--port", "7078",
	})
	if err != nil {
		t.Fatalf("parse options: %v", err)
	}
	if options.ConfigDir != "/tmp/config" {
		t.Fatalf("expected config dir, got %q", options.ConfigDir)
	}
	if options.RuntimeDir != "/tmp/runtime" {
		t.Fatalf("expected runtime dir, got %q", options.RuntimeDir)
	}
	if options.Port != "7078" {
		t.Fatalf("expected port 7078, got %q", options.Port)
	}
}

func TestParseConfigOptionsRejectsUnexpectedArgs(t *testing.T) {
	if _, err := parseConfigOptions([]string{"run"}); err == nil {
		t.Fatal("expected unexpected argument error")
	}
}
