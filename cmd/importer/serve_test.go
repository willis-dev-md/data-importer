package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/willis-dev-md/data-importer/internal/testutil"
)

func TestServeConfigErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"cert without key", map[string]string{"TLS_CERT_FILE": "c", "DATABASE_URL": "postgres://x"}, "TLS_KEY_FILE"},
		{"key without cert", map[string]string{"TLS_KEY_FILE": "k", "DATABASE_URL": "postgres://x"}, "TLS_CERT_FILE"},
		{"no database url", map[string]string{}, "DATABASE_URL is not set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var errb bytes.Buffer
			code := serve(context.Background(), nil, func(k string) string { return tt.env[k] }, &errb)
			if code != 1 || !strings.Contains(errb.String(), tt.want) {
				t.Errorf("code = %d, stderr = %q, want 1 containing %q", code, errb.String(), tt.want)
			}
		})
	}
}

func TestServeStopsOnContextCancel(t *testing.T) {
	url := testutil.NewDatabaseURL(t)
	env := map[string]string{"DATABASE_URL": url, "GRPC_ADDR": "127.0.0.1:0", "FILE_STORE_DIR": t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	var errb bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- serve(ctx, nil, func(k string) string { return env[k] }, &errb) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("code = %d, stderr = %s", code, errb.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
}
