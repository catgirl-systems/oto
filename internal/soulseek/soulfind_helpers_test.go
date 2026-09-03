package soulseek

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func soulfindAddress(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("OTO_SOULFIND_ADDR")
	if addr == "" {
		t.Skip("OTO_SOULFIND_ADDR is unset")
	}
	return addr
}

func startSoulfindClient(t *testing.T, addr, username string, files map[string][]byte, uploads *UploadManager) *Client {
	t.Helper()
	shares := NewShareIndex()
	if len(files) > 0 {
		root := t.TempDir()
		for name, contents := range files {
			if err := os.WriteFile(filepath.Join(root, name), contents, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := shares.AddRoot("Music", root); err != nil {
			t.Fatal(err)
		}
		if err := shares.ScanContext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	client := NewClient(ClientConfig{Address: addr, Username: username, Password: "pw", ListenAddr: "0.0.0.0:0", Share: shares, Uploads: uploads})
	t.Cleanup(func() { _ = client.Close() })
	connectSoulfind(t, client)
	runSoulfind(client)
	return client
}

func runSoulfind(client *Client) <-chan error {
	done := make(chan error, 1)
	go func() { done <- client.Run(context.Background()) }()
	return done
}
func connectSoulfind(t *testing.T, client *Client) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := client.Connect(ctx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect to Soulfind: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Login(ctx); err != nil {
		t.Fatalf("login to Soulfind: %v", err)
	}
}
