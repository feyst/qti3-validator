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

func TestConfigSpoolAndQueue(t *testing.T) {
	d := Default()
	if d.MaxSpoolSize != 4*d.MaxPackageSize || d.MaxQueue != 4*d.MaxConcurrent {
		t.Fatalf("defaults: spool %d, queue %d", d.MaxSpoolSize, d.MaxQueue)
	}

	// Unset, they follow the settings they are derived from.
	t.Setenv("MAX_PACKAGE_SIZE", "1000")
	t.Setenv("MAX_CONCURRENT", "3")
	cfg, err := FromEnv()
	if err != nil || cfg.MaxSpoolSize != 4000 || cfg.MaxQueue != 12 {
		t.Fatalf("derived: %+v, %v", cfg, err)
	}

	t.Setenv("MAX_SPOOL_SIZE", "5000")
	t.Setenv("MAX_QUEUE", "2")
	cfg, err = FromEnv()
	if err != nil || cfg.MaxSpoolSize != 5000 || cfg.MaxQueue != 2 {
		t.Fatalf("set: %+v, %v", cfg, err)
	}

	for _, name := range []string{"MAX_SPOOL_SIZE", "MAX_QUEUE"} {
		for _, bad := range []string{"0", "-1", "x"} {
			t.Run(name+"="+bad, func(t *testing.T) {
				t.Setenv(name, bad)
				if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("want an error for %s, got %v", name, err)
				}
			})
		}
	}
}
