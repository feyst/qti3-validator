package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"qti3-validator/internal/config"
)

// runHealthcheck asks the service on this machine for /health, so the image
// can declare a HEALTHCHECK without a shell or curl in it.
func runHealthcheck() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	url, err := healthURL(cfg.Addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return nil
}

// healthURL turns the listen address into the URL of /health on loopback: a
// service that listens on all interfaces, ":8080" or "0.0.0.0:8080", is
// reached on 127.0.0.1.
func healthURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("ADDR %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/health", nil
}
