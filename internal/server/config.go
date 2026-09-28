package server

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"time"
)

// Config holds the service settings, read from environment variables.
type Config struct {
	Addr                string        // ADDR
	MaxRequestSize      int64         // MAX_REQUEST_SIZE: body of /v1/validate, bytes
	MaxPackageSize      int64         // MAX_PACKAGE_SIZE: body of /v1/validate/package, bytes
	MaxFileSize         int64         // MAX_FILE_SIZE: uncompressed XML entry in a package, bytes
	MaxFiles            int           // MAX_FILES: entries in a package
	MaxUncompressedSize int64         // MAX_UNCOMPRESSED_SIZE: all XML in a package, bytes
	MaxErrors           int           // MAX_ERRORS: validation errors per document
	MaxDepth            int           // MAX_DEPTH: XML element nesting
	MaxConcurrent       int           // MAX_CONCURRENT: validations running at once
	RequestTimeout      time.Duration // REQUEST_TIMEOUT
	ValidatorsDir       string        // VALIDATORS_DIR: extra .xsd and .sch files
	// ValidatorsDirSet reports that VALIDATORS_DIR was set explicitly. Then
	// the directory must exist; the default may be missing.
	ValidatorsDirSet bool
}

// DefaultValidatorsDir is where the image provides an empty directory for
// extra validators, so mounting a directory there is all it takes.
const DefaultValidatorsDir = "/validators"

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{
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
}

// ConfigFromEnv returns the defaults overridden by environment variables.
func ConfigFromEnv() (Config, error) {
	c := DefaultConfig()
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
