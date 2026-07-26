package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor/internal/buildinfo"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestStartWithoutStartupCheckDoesNotSendRequest(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	manager := NewManager(nil)
	manager.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		requests++
		mu.Unlock()
		return nil, context.Canceled
	})}

	manager.Start(false)
	t.Cleanup(manager.Shutdown)
	time.Sleep(25 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if requests != 0 {
		t.Fatalf("startup sent %d update requests, want 0", requests)
	}
}

func TestManualCheckRequiresConfirmationBeforeDownload(t *testing.T) {
	originalVersion := buildinfo.Version
	buildinfo.Version = "1.0.0"
	t.Cleanup(func() { buildinfo.Version = originalVersion })

	payload := []byte("verified update archive")
	checksum := sha256.Sum256(payload)
	platformKey, err := currentPlatformKey()
	if err != nil {
		t.Skipf("unsupported updater test platform %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	assetURL := "https://github.com/leookun/cursor-byok/releases/download/v9.9.9/update.tar.gz"
	manifestBody, err := json.Marshal(manifest{
		Version:   "9.9.9",
		Mandatory: true,
		Platforms: map[string]manifestPlatform{
			platformKey: {
				URL:      assetURL,
				Size:     int64(len(payload)),
				Checksum: hex.EncodeToString(checksum[:]),
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	var mu sync.Mutex
	var requestURLs []string
	manager := NewManager(nil)
	manager.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		requestURLs = append(requestURLs, request.URL.String())
		mu.Unlock()
		body := manifestBody
		if request.URL.String() == assetURL {
			body = payload
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
			Header:        make(http.Header),
			Request:       request,
		}, nil
	})}
	t.Cleanup(manager.Shutdown)

	manager.CheckNow(true)
	waitForState(t, manager, StateAvailable)
	manager.mu.Lock()
	if manager.currentInfo == nil || !manager.currentInfo.Mandatory {
		manager.mu.Unlock()
		t.Fatal("mandatory manifest metadata was not preserved")
	}
	manager.mu.Unlock()
	mu.Lock()
	if len(requestURLs) != 1 || !strings.HasSuffix(requestURLs[0], "update.json") {
		t.Fatalf("manual check requests = %v, want only manifest", requestURLs)
	}
	mu.Unlock()

	manager.mu.Lock()
	if manager.readyInfo != nil || manager.downloadedPath != "" {
		t.Fatalf("available state must not expose installable package")
	}
	manager.mu.Unlock()
	if err := manager.InstallReadyUpdate(); err == nil {
		t.Fatal("InstallReadyUpdate() succeeded before download confirmation")
	}

	if err := manager.DownloadAvailableUpdate(); err != nil {
		t.Fatalf("DownloadAvailableUpdate() error = %v", err)
	}
	waitForState(t, manager, StateReady)

	mu.Lock()
	if len(requestURLs) != 2 || requestURLs[1] != assetURL {
		t.Fatalf("confirmed download requests = %v", requestURLs)
	}
	mu.Unlock()

	manager.mu.Lock()
	downloadedPath := manager.downloadedPath
	manager.mu.Unlock()
	if _, err := os.Stat(downloadedPath); err != nil {
		t.Fatalf("downloaded archive missing: %v", err)
	}
	manager.Shutdown()
	if _, err := os.Stat(downloadedPath); !os.IsNotExist(err) {
		t.Fatalf("downloaded archive was not cleaned up, stat error = %v", err)
	}
}

func TestValidateUpdateAssetRejectsUnsafeMetadata(t *testing.T) {
	validChecksum := strings.Repeat("a", sha256.Size*2)
	tests := []struct {
		name  string
		asset manifestPlatform
	}{
		{name: "http", asset: manifestPlatform{URL: "http://github.com/leookun/cursor-byok/releases/download/v1/a.zip", Size: 1, Checksum: validChecksum}},
		{name: "foreign host", asset: manifestPlatform{URL: "https://example.com/a.zip", Size: 1, Checksum: validChecksum}},
		{name: "foreign repo", asset: manifestPlatform{URL: "https://github.com/other/repo/releases/download/v1/a.zip", Size: 1, Checksum: validChecksum}},
		{name: "missing checksum", asset: manifestPlatform{URL: "https://github.com/leookun/cursor-byok/releases/download/v1/a.zip", Size: 1}},
		{name: "oversized", asset: manifestPlatform{URL: "https://github.com/leookun/cursor-byok/releases/download/v1/a.zip", Size: maxUpdateArchiveBytes + 1, Checksum: validChecksum}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateUpdateAsset(test.asset); err == nil {
				t.Fatalf("validateUpdateAsset(%+v) succeeded", test.asset)
			}
		})
	}
}

func TestValidateUpdateRedirectHosts(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "github release", url: "https://github.com/leookun/cursor-byok/releases/download/v1/a.tar.gz", want: true},
		{name: "release asset CDN", url: "https://release-assets.githubusercontent.com/object", want: true},
		{name: "github objects CDN", url: "https://objects.githubusercontent.com/object", want: true},
		{name: "http downgrade", url: "http://github.com/leookun/cursor-byok/releases/download/v1/a.tar.gz", want: false},
		{name: "foreign host", url: "https://example.com/object", want: false},
		{name: "custom port", url: "https://github.com:8443/object", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, test.url, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			if got := isAllowedUpdateRedirect(request); got != test.want {
				t.Fatalf("isAllowedUpdateRedirect(%s) = %v, want %v", test.url, got, test.want)
			}
		})
	}
}

func TestUpdateHTTPClientRejectsForeignRedirect(t *testing.T) {
	client := newUpdateHTTPClient(time.Second)
	allowed, err := http.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/object", nil)
	if err != nil {
		t.Fatalf("new allowed redirect request: %v", err)
	}
	if err := client.CheckRedirect(allowed, nil); err != nil {
		t.Fatalf("allowed GitHub asset redirect rejected: %v", err)
	}

	foreign, err := http.NewRequest(http.MethodGet, "https://example.com/object", nil)
	if err != nil {
		t.Fatalf("new foreign redirect request: %v", err)
	}
	if err := client.CheckRedirect(foreign, nil); err == nil {
		t.Fatal("foreign redirect was accepted")
	}
}

func TestShutdownCancelsInFlightDownloadWithoutReadyArchive(t *testing.T) {
	manager := NewManager(nil)
	started := make(chan struct{})
	manager.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	manager.mu.Lock()
	manager.state = StateAvailable
	manager.currentInfo = &UpdateInfo{
		Version: "9.9.9",
		Asset: manifestPlatform{
			URL:      "https://github.com/leookun/cursor-byok/releases/download/v9.9.9/update.tar.gz",
			Size:     1,
			Checksum: strings.Repeat("a", sha256.Size*2),
		},
	}
	manager.mu.Unlock()
	if err := manager.DownloadAvailableUpdate(); err != nil {
		t.Fatalf("DownloadAvailableUpdate() error = %v", err)
	}
	<-started
	manager.Shutdown()
	time.Sleep(20 * time.Millisecond)

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.downloadedPath != "" || manager.readyInfo != nil {
		t.Fatalf("shutdown left installable archive: path=%q ready=%v", manager.downloadedPath, manager.readyInfo != nil)
	}
}

func TestDownloadRejectsManifestSizeMismatch(t *testing.T) {
	payload := []byte("verified update archive")
	checksum := sha256.Sum256(payload)
	assetURL := "https://github.com/leookun/cursor-byok/releases/download/v9.9.9/update.tar.gz"
	manager := NewManager(nil)
	manager.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(bytes.NewReader(payload)),
			ContentLength: -1,
			Header:        make(http.Header),
			Request:       request,
		}, nil
	})}
	t.Cleanup(manager.Shutdown)

	_, err := manager.downloadUpdate(context.Background(), &UpdateInfo{
		Version: "9.9.9",
		Asset: manifestPlatform{
			URL:      assetURL,
			Size:     int64(len(payload) + 1),
			Checksum: hex.EncodeToString(checksum[:]),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("downloadUpdate() error = %v, want manifest size error", err)
	}
}

func waitForState(t *testing.T, manager *Manager, wanted State) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		state := manager.state
		manager.mu.Unlock()
		if state == wanted {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	manager.mu.Lock()
	state := manager.state
	manager.mu.Unlock()
	t.Fatalf("state = %s, want %s", state, wanted)
}
