package main

import "testing"

func TestParseConfigOptions(t *testing.T) {
	options, err := parseConfigOptions([]string{
		"--config-dir", "/tmp/config",
		"--port", "7078",
	})
	if err != nil {
		t.Fatalf("parse options: %v", err)
	}
	if options.ConfigDir != "/tmp/config" {
		t.Fatalf("expected config dir, got %q", options.ConfigDir)
	}
	if options.Port != "7078" {
		t.Fatalf("expected port 7078, got %q", options.Port)
	}
}

func TestParseConfigOptionsRejectsRuntimeDir(t *testing.T) {
	if _, err := parseConfigOptions([]string{"--runtime-dir", "/tmp/runtime"}); err == nil {
		t.Fatal("expected runtime-dir flag error")
	}
}

func TestParseConfigOptionsRejectsUnexpectedArgs(t *testing.T) {
	if _, err := parseConfigOptions([]string{"run"}); err == nil {
		t.Fatal("expected unexpected argument error")
	}
}
