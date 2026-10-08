package main

import "testing"

func TestHealthURL(t *testing.T) {
	for addr, want := range map[string]string{
		":8080":          "http://127.0.0.1:8080/health",
		"0.0.0.0:9000":   "http://127.0.0.1:9000/health",
		"[::]:8080":      "http://127.0.0.1:8080/health",
		"127.0.0.1:8080": "http://127.0.0.1:8080/health",
		"[::1]:8080":     "http://[::1]:8080/health",
		"localhost:8080": "http://localhost:8080/health",
	} {
		got, err := healthURL(addr)
		if err != nil || got != want {
			t.Errorf("healthURL(%q) = %q, %v; want %q", addr, got, err, want)
		}
	}
	if _, err := healthURL("8080"); err == nil {
		t.Error(`healthURL("8080") gave no error`)
	}
}
