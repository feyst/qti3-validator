package config

import (
	"strings"
	"testing"
)

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("MAX_FILES", "7")
	t.Setenv("REQUEST_TIMEOUT", "3s")
	t.Setenv("VALIDATORS_DIR", "/validators")
	cfg, err := FromEnv()
	if err != nil || cfg.MaxFiles != 7 || cfg.RequestTimeout.String() != "3s" || cfg.ValidatorsDir != "/validators" {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	if !cfg.ValidatorsDirSet {
		t.Fatal("VALIDATORS_DIR set, but ValidatorsDirSet is false")
	}
	if d := Default(); d.ValidatorsDir != DefaultValidatorsDir || d.ValidatorsDirSet {
		t.Fatalf("default: %+v", d)
	}
	t.Setenv("MAX_PACKAGE_SIZE", "-1")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "MAX_PACKAGE_SIZE") {
		t.Fatalf("want an error for MAX_PACKAGE_SIZE, got %v", err)
	}
}
