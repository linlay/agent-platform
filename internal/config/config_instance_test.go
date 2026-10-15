package config

import (
	"strings"
	"testing"
)

func TestRuntimeFileInstanceAuthAndPublicOrigin(t *testing.T) {
	c := &Config{}
	yaml := "auth:\n  audience: instance-a\n  allowed-subjects: [owner, admin]\n  require-expiry: true\n" +
		"resource:\n  public-base-url: https://linlay.ap.example.com/\n"
	if err := c.applyRuntimeFile(configFixture(t, "runtime.yml", yaml)); err != nil {
		t.Fatalf("applyRuntimeFile: %v", err)
	}
	if c.Auth.Audience != "instance-a" || !c.Auth.RequireExpiry || strings.Join(c.Auth.AllowedSubjects, ",") != "owner,admin" {
		t.Fatalf("auth = %+v", c.Auth)
	}
	if c.Server.PublicBaseURL != "https://linlay.ap.example.com" {
		t.Fatalf("public base url = %q", c.Server.PublicBaseURL)
	}
}

func TestRuntimeFileRejectsInvalidPublicBaseURL(t *testing.T) {
	for _, yaml := range []string{
		"resource:\n  public-base-url: linlay.ap.example.com\n",
		"resource:\n  public-base-url: https://linlay.ap.example.com/app\n",
		"resource:\n  public-base-url: https://user:pass@linlay.ap.example.com\n",
		"resource:\n  public-base-url: ftp://linlay.ap.example.com\n",
	} {
		if err := (&Config{}).applyRuntimeFile(configFixture(t, "runtime.yml", yaml)); err == nil {
			t.Fatalf("expected %q to be rejected", yaml)
		}
	}
}
