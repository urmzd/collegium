package main

import "testing"

// Dev auth makes every request an admin, so it must never be reachable from
// another machine.
func TestDevAuthIsLoopbackOnly(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true,
		"localhost:8080": true,
		"[::1]:8080":     true,
		"0.0.0.0:8080":   false,
		":8080":          false,
		"10.0.0.5:8080":  false,
		"example.com:80": false,
		"nonsense":       false,
	} {
		if got := loopback(addr); got != want {
			t.Errorf("loopback(%q) = %v, want %v", addr, got, want)
		}
	}
	for _, addr := range []string{"0.0.0.0:8080", ":8080"} {
		if err := run([]string{"serve", "-auth=dev", "-addr", addr}); err == nil {
			t.Errorf("serve -auth=dev -addr %s started", addr)
		}
	}
}
