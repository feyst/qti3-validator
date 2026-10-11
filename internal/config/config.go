// Package config reads the service settings from environment variables.
package config

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"time"
)

// Config holds the service settings, read from environment variables.
type Config struct {
	Addr                string        // ADDR
	MaxRequestSize      int64         // MAX_REQUEST_SIZE: one XML document, bytes
	MaxPackageSize      int64         // MAX_PACKAGE_SIZE: request body of a package or upload, bytes
	MaxFileSize         int64         // MAX_FILE_SIZE: uncompressed XML entry in a package, bytes
	MaxFiles            int           // MAX_FILES: entries in a package
	MaxUncompressedSize int64         // MAX_UNCOMPRESSED_SIZE: all XML in a package, bytes
	MaxErrors           int           // MAX_ERRORS: validation errors per document
	MaxDepth            int           // MAX_DEPTH: XML element nesting
	MaxConcurrent       int           // MAX_CONCURRENT: validations running at once
	MaxQueue            int           // MAX_QUEUE: requests waiting for a validation slot
	MaxSpoolSize        int64         // MAX_SPOOL_SIZE: all request bodies being received or validated, bytes
	MaxValidationMemory int64         // MAX_VALIDATION_MEMORY: heap the running validations may reserve, bytes; 0 derives it at startup from the memory limit
	RequestTimeout      time.Duration // REQUEST_TIMEOUT
	ValidatorsDir       string        // VALIDATORS_DIR: extra .xsd and .sch files
	// ValidatorsDirSet reports that VALIDATORS_DIR was set explicitly. Then
	// the directory must exist; the default may be missing.
	ValidatorsDirSet bool
}

// DefaultValidatorsDir is where the image provides an empty directory for
// extra validators, so mounting a directory there is all it takes.
const DefaultValidatorsDir = "/validators"

// Default returns the defaults.
func Default() Config {
	c := Config{
		Addr:                ":8080",
		MaxRequestSize:      10 << 20,
		MaxPackageSize:      100 << 20,
		MaxFileSize:         10 << 20,
		MaxFiles:            1000,
		MaxUncompressedSize: 256 << 20,
		MaxErrors:           100,
		MaxDepth:            256,
		MaxConcurrent:       runtime.GOMAXPROCS(0),
		RequestTimeout:      60 * time.Second,
		ValidatorsDir:       DefaultValidatorsDir,
	}
	c.deriveDefaults()
	return c
}

// deriveDefaults sets the settings whose defaults follow from others: room
// to spool four packages of MAX_PACKAGE_SIZE, and four waiting requests per
// slot.
func (c *Config) deriveDefaults() {
	c.MaxSpoolSize = 4 * min(c.MaxPackageSize, math.MaxInt64/4)
	c.MaxQueue = 4 * min(c.MaxConcurrent, math.MaxInt32/4)
}

// FromEnv returns the defaults overridden by environment variables.
func FromEnv() (Config, error) {
	c := Default()
	if v := os.Getenv("ADDR"); v != "" {
		c.Addr = v
	}
	ints := []struct {
		name string
		dst  *int64
	}{
		{"MAX_REQUEST_SIZE", &c.MaxRequestSize},
		{"MAX_PACKAGE_SIZE", &c.MaxPackageSize},
		{"MAX_FILE_SIZE", &c.MaxFileSize},
		{"MAX_UNCOMPRESSED_SIZE", &c.MaxUncompressedSize},
	}
	for _, i := range ints {
		if err := positiveEnv(i.name, i.dst); err != nil {
			return c, err
		}
	}
	for _, i := range []struct {
		name string
		dst  *int
	}{
		{"MAX_FILES", &c.MaxFiles},
		{"MAX_ERRORS", &c.MaxErrors},
		{"MAX_DEPTH", &c.MaxDepth},
		{"MAX_CONCURRENT", &c.MaxConcurrent},
	} {
		v := int64(*i.dst)
		if err := positiveEnv(i.name, &v); err != nil {
			return c, err
		}
		*i.dst = int(v)
	}
	c.deriveDefaults()
	if err := positiveEnv("MAX_SPOOL_SIZE", &c.MaxSpoolSize); err != nil {
		return c, err
	}
	queue := int64(c.MaxQueue)
	if err := positiveEnv("MAX_QUEUE", &queue); err != nil {
		return c, err
	}
	c.MaxQueue = int(queue)
	// MAX_VALIDATION_MEMORY stays 0 when unset: the composition root then
	// derives it from the container's memory limit at startup.
	if err := positiveEnv("MAX_VALIDATION_MEMORY", &c.MaxValidationMemory); err != nil {
		return c, err
	}
	if v, ok := os.LookupEnv("VALIDATORS_DIR"); ok {
		c.ValidatorsDir, c.ValidatorsDirSet = v, true
	}
	if v := os.Getenv("REQUEST_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return c, fmt.Errorf("REQUEST_TIMEOUT: want a positive duration such as 60s, got %q", v)
		}
		c.RequestTimeout = d
	}
	return c, nil
}

func positiveEnv(name string, dst *int64) error {
	v := os.Getenv(name)
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return fmt.Errorf("%s: want a positive integer, got %q", name, v)
	}
	*dst = n
	return nil
}
