package soulseek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSoulfindSlskdDownload(t *testing.T) {
	addr := soulfindAddress(t)
	api, peerUser, downloadRoot := os.Getenv("OTO_SLSKD_API"), os.Getenv("OTO_SLSKD_USERNAME"), os.Getenv("OTO_SLSKD_DOWNLOAD_DIR")
	if api == "" || peerUser == "" || downloadRoot == "" {
		t.Skip("OTO_SLSKD_API, OTO_SLSKD_USERNAME, and OTO_SLSKD_DOWNLOAD_DIR must be set")
	}

	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	filename := "slskd-" + stamp + ".flac"
	contents := bytes.Repeat([]byte("oto upload interoperability\n"), 128)
	target := startSoulfindClient(t, addr, "x"+stamp, map[string][]byte{filename: contents}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	lastAPI, err := waitForSlskd(ctx, api)
	if err != nil {
		t.Fatalf("wait for slskd: %v (last response: %s)", err, lastAPI)
	}
	payload, err := json.Marshal([]struct {
		Filename string `json:"filename"`
		Size     uint64 `json:"size"`
	}{{Filename: "Music\\" + filename, Size: uint64(len(contents))}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(api, "/")+"/api/v0/transfers/downloads/"+target.cfg.Username, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	lastAPI = fmt.Sprintf("%s: %s", response.Status, strings.TrimSpace(string(body)))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("slskd download request: %s", lastAPI)
	}

	var lastUpload TransferEvent
	for lastUpload.State != "completed" {
		select {
		case <-ctx.Done():
			t.Fatalf("slskd download timed out: %v (last response: %s; last upload: %+v)", ctx.Err(), lastAPI, lastUpload)
		case event := <-target.Events():
			transfer, ok := event.Message.(TransferEvent)
			if !ok || transfer.Direction != "upload" || transfer.Username != peerUser || transfer.Filename != "Music\\"+filename {
				continue
			}
			lastUpload = transfer
			if transfer.State == "failed" {
				t.Fatalf("oto upload failed: %+v (slskd response: %s)", transfer, lastAPI)
			}
		}
	}
	if lastUpload.Done != uint64(len(contents)) || lastUpload.Total != uint64(len(contents)) || lastUpload.Error != "" {
		t.Fatalf("completed upload: %+v", lastUpload)
	}

	var lastFileErr error
	for {
		path, err := findSlskdDownload(downloadRoot, filename)
		if err == nil {
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, contents) {
				t.Fatalf("slskd downloaded %d bytes, want %d", len(got), len(contents))
			}
			return
		}
		lastFileErr = err
		select {
		case <-ctx.Done():
			t.Fatalf("slskd did not persist download: %v (last response: %s; last upload: %+v)", lastFileErr, lastAPI, lastUpload)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func waitForSlskd(ctx context.Context, api string) (string, error) {
	endpoint := strings.TrimRight(api, "/") + "/api/v0/server"
	last := "no response"
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return last, err
		}
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			last = fmt.Sprintf("%s: %s", response.Status, strings.TrimSpace(string(body)))
			var state struct {
				Connected bool `json:"isConnected"`
				LoggedIn  bool `json:"isLoggedIn"`
			}
			if response.StatusCode == http.StatusOK && readErr == nil && json.Unmarshal(body, &state) == nil && state.Connected && state.LoggedIn {
				return last, nil
			}
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func findSlskdDownload(root, name string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == name {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", os.ErrNotExist
	}
	return found, nil
}
