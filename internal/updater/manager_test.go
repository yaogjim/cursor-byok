package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

func TestUpdateSourceUsesForkRepository(t *testing.T) {
	const (
		forkRepo       = "yaogjim/cursor-byok"
		manifestURL    = "https://github.com/yaogjim/cursor-byok/releases/latest/download/update.json"
		releasePageURL = "https://github.com/yaogjim/cursor-byok/releases"
		forkAssetURL   = "https://github.com/yaogjim/cursor-byok/releases/download/v1.0.0/update.tar.gz"
		otherAssetURL  = "https://github.com/example-org/cursor-byok/releases/download/v1.0.0/update.tar.gz"
	)

	if buildinfo.ReleaseRepo != forkRepo {
		t.Fatalf("ReleaseRepo = %q, want %q", buildinfo.ReleaseRepo, forkRepo)
	}
	if got := buildinfo.UpdateBaseURL + "update.json"; got != manifestURL {
		t.Fatalf("update manifest URL = %q, want %q", got, manifestURL)
	}
	if buildinfo.ReleasePageURL != releasePageURL {
		t.Fatalf("ReleasePageURL = %q, want %q", buildinfo.ReleasePageURL, releasePageURL)
	}

	validChecksum := strings.Repeat("a", sha256.Size*2)
	if err := validateUpdateAsset(manifestPlatform{URL: forkAssetURL, Size: 1, Checksum: validChecksum}); err != nil {
		t.Fatalf("fork release asset rejected: %v", err)
	}
	if err := validateUpdateAsset(manifestPlatform{URL: otherAssetURL, Size: 1, Checksum: validChecksum}); err == nil {
		t.Fatal("non-release repository asset was accepted")
	}
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
	assetURL := "https://github.com/yaogjim/cursor-byok/releases/download/v9.9.9/update.tar.gz"
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
		{name: "http", asset: manifestPlatform{URL: "http://github.com/yaogjim/cursor-byok/releases/download/v1/a.zip", Size: 1, Checksum: validChecksum}},
		{name: "foreign host", asset: manifestPlatform{URL: "https://example.com/a.zip", Size: 1, Checksum: validChecksum}},
		{name: "foreign repo", asset: manifestPlatform{URL: "https://github.com/other/repo/releases/download/v1/a.zip", Size: 1, Checksum: validChecksum}},
		{name: "missing checksum", asset: manifestPlatform{URL: "https://github.com/yaogjim/cursor-byok/releases/download/v1/a.zip", Size: 1}},
		{name: "oversized", asset: manifestPlatform{URL: "https://github.com/yaogjim/cursor-byok/releases/download/v1/a.zip", Size: maxUpdateArchiveBytes + 1, Checksum: validChecksum}},
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
		{name: "github release", url: "https://github.com/yaogjim/cursor-byok/releases/download/v1/a.tar.gz", want: true},
		{name: "release asset CDN", url: "https://release-assets.githubusercontent.com/object", want: true},
		{name: "github objects CDN", url: "https://objects.githubusercontent.com/object", want: true},
		{name: "http downgrade", url: "http://github.com/yaogjim/cursor-byok/releases/download/v1/a.tar.gz", want: false},
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
			URL:      "https://github.com/yaogjim/cursor-byok/releases/download/v9.9.9/update.tar.gz",
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
	shortenRetryDelays(t)
	payload := []byte("verified update archive")
	checksum := sha256.Sum256(payload)
	assetURL := "https://github.com/yaogjim/cursor-byok/releases/download/v9.9.9/update.tar.gz"
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

// shortenRetryDelays 让重试退避在测试中几乎不耗时。
func shortenRetryDelays(t *testing.T) {
	t.Helper()
	originalBase, originalMax := retryBaseDelay, retryMaxDelay
	retryBaseDelay = time.Millisecond
	retryMaxDelay = 2 * time.Millisecond
	t.Cleanup(func() {
		retryBaseDelay = originalBase
		retryMaxDelay = originalMax
	})
}

// truncatedReader 先返回 data 再返回 err，用于模拟响应体被提前截断。
type truncatedReader struct {
	data []byte
	pos  int
	err  error
}

func (r *truncatedReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func TestRetryDelayGrowsAndIsCapped(t *testing.T) {
	originalBase, originalMax := retryBaseDelay, retryMaxDelay
	retryBaseDelay = 100 * time.Millisecond
	retryMaxDelay = 350 * time.Millisecond
	t.Cleanup(func() {
		retryBaseDelay = originalBase
		retryMaxDelay = originalMax
	})

	if got := retryDelay(1); got != 0 {
		t.Fatalf("retryDelay(1) = %s, want 0", got)
	}
	if got := retryDelay(2); got != 100*time.Millisecond {
		t.Fatalf("retryDelay(2) = %s, want 100ms", got)
	}
	if got := retryDelay(3); got != 200*time.Millisecond {
		t.Fatalf("retryDelay(3) = %s, want 200ms", got)
	}
	if got := retryDelay(4); got != 350*time.Millisecond {
		t.Fatalf("retryDelay(4) = %s, want capped 350ms", got)
	}
}

func TestDownloadResumesAfterTruncatedResponse(t *testing.T) {
	shortenRetryDelays(t)

	payload := bytes.Repeat([]byte("cursor-byok-update-"), 97)
	checksum := sha256.Sum256(payload)
	cut := len(payload) / 2
	assetURL := "https://github.com/yaogjim/cursor-byok/releases/download/v9.9.9/update.tar.gz"

	var mu sync.Mutex
	var ranges []string
	attempt := 0
	manager := NewManager(nil)
	t.Cleanup(manager.Shutdown)
	manager.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		attempt++
		current := attempt
		ranges = append(ranges, request.Header.Get("Range"))
		mu.Unlock()

		if current == 1 {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Body:          io.NopCloser(&truncatedReader{data: payload[:cut], err: io.ErrUnexpectedEOF}),
				ContentLength: int64(len(payload)),
				Header:        make(http.Header),
				Request:       request,
			}, nil
		}

		rest := payload[cut:]
		header := make(http.Header)
		header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(payload)-1, len(payload)))
		return &http.Response{
			StatusCode:    http.StatusPartialContent,
			Status:        "206 Partial Content",
			Body:          io.NopCloser(bytes.NewReader(rest)),
			ContentLength: int64(len(rest)),
			Header:        header,
			Request:       request,
		}, nil
	})}

	archivePath, err := manager.downloadUpdate(context.Background(), &UpdateInfo{
		Version: "9.9.9",
		Asset: manifestPlatform{
			URL:      assetURL,
			Size:     int64(len(payload)),
			Checksum: hex.EncodeToString(checksum[:]),
		},
	})
	if err != nil {
		t.Fatalf("downloadUpdate() error = %v, want success after resume", err)
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })

	written, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read downloaded archive: %v", err)
	}
	if !bytes.Equal(written, payload) {
		t.Fatalf("downloaded archive mismatch: got %d bytes, want %d", len(written), len(payload))
	}

	mu.Lock()
	defer mu.Unlock()
	wantRange := fmt.Sprintf("bytes=%d-", cut)
	if len(ranges) != 2 || ranges[0] != "" || ranges[1] != wantRange {
		t.Fatalf("Range headers = %v, want [\"\" %q]", ranges, wantRange)
	}
}

func TestDownloadRestartsFromScratchAfterChecksumMismatch(t *testing.T) {
	shortenRetryDelays(t)

	payload := bytes.Repeat([]byte("verified update archive-"), 53)
	checksum := sha256.Sum256(payload)
	corrupted := append([]byte(nil), payload...)
	corrupted[7] ^= 0xff
	assetURL := "https://github.com/yaogjim/cursor-byok/releases/download/v9.9.9/update.tar.gz"

	var mu sync.Mutex
	var ranges []string
	attempt := 0
	manager := NewManager(nil)
	t.Cleanup(manager.Shutdown)
	manager.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		attempt++
		current := attempt
		ranges = append(ranges, request.Header.Get("Range"))
		mu.Unlock()

		body := payload
		if current == 1 {
			body = corrupted
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

	archivePath, err := manager.downloadUpdate(context.Background(), &UpdateInfo{
		Version: "9.9.9",
		Asset: manifestPlatform{
			URL:      assetURL,
			Size:     int64(len(payload)),
			Checksum: hex.EncodeToString(checksum[:]),
		},
	})
	if err != nil {
		t.Fatalf("downloadUpdate() error = %v, want success after restart", err)
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })

	written, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read downloaded archive: %v", err)
	}
	if !bytes.Equal(written, payload) {
		t.Fatal("re-downloaded archive does not match manifest payload")
	}

	mu.Lock()
	defer mu.Unlock()
	// 校验失败后不能续传被污染的残片：第二次请求必须从头下载
	if len(ranges) != 2 || ranges[0] != "" || ranges[1] != "" {
		t.Fatalf("Range headers = %v, want two full downloads", ranges)
	}
}

func TestFetchUpdateInfoRetriesTruncatedManifest(t *testing.T) {
	shortenRetryDelays(t)

	originalVersion := buildinfo.Version
	buildinfo.Version = "1.0.0"
	t.Cleanup(func() { buildinfo.Version = originalVersion })

	platformKey, err := currentPlatformKey()
	if err != nil {
		t.Skipf("unsupported updater test platform %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	manifestBody, err := json.Marshal(manifest{
		Version: "9.9.9",
		Platforms: map[string]manifestPlatform{
			platformKey: {
				URL:      "https://github.com/yaogjim/cursor-byok/releases/download/v9.9.9/update.tar.gz",
				Size:     16,
				Checksum: strings.Repeat("a", sha256.Size*2),
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	var mu sync.Mutex
	attempt := 0
	manager := NewManager(nil)
	t.Cleanup(manager.Shutdown)
	manager.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		attempt++
		current := attempt
		mu.Unlock()

		if current == 1 {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Body:          io.NopCloser(&truncatedReader{data: manifestBody[:len(manifestBody)/2], err: io.ErrUnexpectedEOF}),
				ContentLength: int64(len(manifestBody)),
				Header:        make(http.Header),
				Request:       request,
			}, nil
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(bytes.NewReader(manifestBody)),
			ContentLength: int64(len(manifestBody)),
			Header:        make(http.Header),
			Request:       request,
		}, nil
	})}

	info, err := manager.fetchUpdateInfo(context.Background())
	if err != nil {
		t.Fatalf("fetchUpdateInfo() error = %v, want success after retry", err)
	}
	if info == nil || info.Version != "9.9.9" {
		t.Fatalf("fetchUpdateInfo() = %+v, want version 9.9.9", info)
	}

	mu.Lock()
	defer mu.Unlock()
	if attempt != 2 {
		t.Fatalf("manifest attempts = %d, want 2", attempt)
	}
}

func TestDownloadErrorExplainsRetryExhaustion(t *testing.T) {
	shortenRetryDelays(t)

	manager := NewManager(nil)
	t.Cleanup(manager.Shutdown)
	manager.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	})}

	_, err := manager.downloadUpdate(context.Background(), &UpdateInfo{
		Version: "9.9.9",
		Asset: manifestPlatform{
			URL:      "https://github.com/yaogjim/cursor-byok/releases/download/v9.9.9/update.tar.gz",
			Size:     16,
			Checksum: strings.Repeat("a", sha256.Size*2),
		},
	})
	if err == nil {
		t.Fatal("downloadUpdate() succeeded on a permanently broken connection")
	}
	message := err.Error()
	if !strings.Contains(message, fmt.Sprintf("已尝试 %d 次", downloadMaxAttempts)) {
		t.Fatalf("error = %q, want attempt count", message)
	}
	if !strings.Contains(message, "网络连接") {
		t.Fatalf("error = %q, want network hint", message)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want wrapped unexpected EOF", err)
	}
}
