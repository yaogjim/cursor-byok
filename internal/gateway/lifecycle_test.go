package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/backend/forwarder"
)

func awaitLifecycle[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(8 * time.Second):
		t.Fatal("lifecycle event timed out")
	}
	var zero T
	return zero
}

func TestGatewayStartMissingTokenRecordsError(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	cfg.Gateway.Token = ""
	cfg.Gateway.ListenAddr = "127.0.0.1:0"
	server := New(nil, nil)
	if err := server.Start(cfg.Gateway); err == nil || server.LastError() == "" {
		t.Fatalf("start err=%v lastError=%q", err, server.LastError())
	}
}

func TestGatewayGracefulDrainCompletesRequests(t *testing.T) {
	for _, path := range []string{chatCompletionsPath, responsesPath} {
		for _, replace := range []bool{false, true} {
			t.Run(path+map[bool]string{false: "/stop", true: "/replace"}[replace], func(t *testing.T) {
				cfg, _ := gatewayTestConfig(t, "secret-token")
				cfg.Gateway.ListenAddr = "127.0.0.1:0"
				started := make(chan context.Context, 1)
				release := make(chan struct{})
				var once sync.Once
				fake := &fakeStreamer{start: func(ctx context.Context, _ forwarder.ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
					started <- ctx
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-release:
					}
					return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "drained successfully"})
				}}
				server := New(fake, staticConfig{cfg: cfg})
				if err := server.Start(cfg.Gateway); err != nil {
					t.Fatal(err)
				}
				defer func() { once.Do(func() { close(release) }); _ = server.Stop(context.Background()) }()
				draining := make(chan struct{})
				server.mu.Lock()
				server.httpServer.RegisterOnShutdown(func() { close(draining) })
				server.mu.Unlock()
				body := `{"model":"public-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`
				if path == responsesPath {
					body = `{"model":"public-a","stream":true,"input":"hi"}`
				}
				req, _ := http.NewRequest(http.MethodPost, "http://"+server.ListenAddr()+path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer secret-token")
				req.Header.Set("Content-Type", "application/json")
				client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}
				defer client.CloseIdleConnections()
				response, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				providerCtx := awaitLifecycle(t, started)
				finished := make(chan error, 1)
				go func() {
					if replace {
						finished <- server.Start(cfg.Gateway)
					} else {
						finished <- server.Stop(context.Background())
					}
				}()
				awaitLifecycle(t, draining)
				if providerCtx.Err() != nil {
					t.Fatalf("drain canceled request early: %v", providerCtx.Err())
				}
				once.Do(func() { close(release) })
				raw, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				marker := "data: [DONE]"
				if path == responsesPath {
					marker = "response.completed"
				}
				if !strings.Contains(string(raw), "drained successfully") || !strings.Contains(string(raw), marker) {
					t.Fatalf("incomplete response: %s", raw)
				}
				if err := awaitLifecycle(t, finished); err != nil {
					t.Fatal(err)
				}
				if server.Running() != replace || server.LastError() != "" {
					t.Fatal("unexpected final state")
				}
			})
		}
	}
}

func TestGatewayBindFailureKeepsOldListenerAvailable(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	cfg.Gateway.ListenAddr = "127.0.0.1:0"
	server := New(&fakeStreamer{}, staticConfig{cfg: cfg})
	if err := server.Start(cfg.Gateway); err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())
	oldAddr := server.ListenAddr()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	cfg.Gateway.ListenAddr = occupied.Addr().String()
	if err := server.Start(cfg.Gateway); err == nil {
		t.Fatal("expected bind failure")
	}
	if !server.Running() || server.ListenAddr() != oldAddr || server.LastError() == "" {
		t.Fatal("old listener state lost")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+oldAddr+modelsPath, nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("old listener returned %d", response.StatusCode)
	}
	cfg.Gateway.ListenAddr = oldAddr
	if err := server.Start(cfg.Gateway); err != nil {
		t.Fatal(err)
	}
	if server.LastError() != "" || server.ListenAddr() != oldAddr {
		t.Fatal("same-address start did not clear error")
	}
}

func TestGatewayReplaceTimeoutStopsBothListeners(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	cfg.Gateway.ListenAddr = "127.0.0.1:0"
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	fake := &fakeStreamer{start: func(ctx context.Context, _ forwarder.ProviderRequest, _ func(modeladapter.ModelEvent) error) error {
		started <- ctx
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}}
	server := New(fake, staticConfig{cfg: cfg})
	if err := server.Start(cfg.Gateway); err != nil {
		t.Fatal(err)
	}
	defer func() { close(release); _ = server.Stop(context.Background()) }()
	oldAddr := server.ListenAddr()
	req, _ := http.NewRequest(http.MethodPost, "http://"+oldAddr+chatCompletionsPath, strings.NewReader(`{"model":"public-a","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	providerCtx := awaitLifecycle(t, started)
	candidate, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	candidateAddr := candidate.Addr().String()
	_ = candidate.Close()
	cfg.Gateway.ListenAddr = candidateAddr
	if err := server.Start(cfg.Gateway); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("replace err=%v", err)
	}
	if server.Running() || server.ListenAddr() != "" || server.LastError() == "" {
		t.Fatalf("failed replacement still running=%v addr=%q error=%q", server.Running(), server.ListenAddr(), server.LastError())
	}
	awaitLifecycle(t, providerCtx.Done())
	for _, addr := range []string{oldAddr, candidateAddr} {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			t.Fatalf("listener still open: %s", addr)
		}
	}
	cfg.Gateway.ListenAddr = oldAddr
	if err := server.Start(cfg.Gateway); err != nil {
		t.Fatal(err)
	}
	if !server.Running() || server.LastError() != "" {
		t.Fatal("restart did not recover")
	}
}

func TestGatewayDelayedOldServeExitDoesNotClearReplacement(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	cfg.Gateway.ListenAddr = "127.0.0.1:0"
	server := New(&fakeStreamer{}, staticConfig{cfg: cfg})
	if err := server.Start(cfg.Gateway); err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())
	replacementListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	replacementCtx, replacementCancel := context.WithCancel(context.Background())
	defer replacementCancel()
	replacement := &http.Server{Handler: server.Handler(), BaseContext: func(net.Listener) context.Context { return replacementCtx }}
	defer replacement.Close()
	// 持锁阻住旧 Serve 的退出回调，确定性模拟新实例先完成安装。
	server.mu.Lock()
	oldCtx := server.httpServer.BaseContext(server.listener)
	_ = server.listener.Close()
	server.httpServer = replacement
	server.listener = replacementListener
	server.requestCancel = replacementCancel
	server.listenAddr = replacementListener.Addr().String()
	server.lastError = ""
	server.mu.Unlock()
	serveDone := make(chan error, 1)
	go func() { serveDone <- replacement.Serve(replacementListener) }()
	defer func() { _ = replacement.Close(); awaitLifecycle(t, serveDone) }()
	awaitLifecycle(t, oldCtx.Done())
	if !server.Running() || server.ListenAddr() != replacementListener.Addr().String() || server.LastError() != "" || replacementCtx.Err() != nil {
		t.Fatal("old Serve exit corrupted replacement")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+server.ListenAddr()+modelsPath, nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("replacement status=%d", response.StatusCode)
	}
}

func TestGatewayUnexpectedServeExitCancelsOwnRequests(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	cfg.Gateway.ListenAddr = "127.0.0.1:0"
	server := New(&fakeStreamer{}, staticConfig{cfg: cfg})
	if err := server.Start(cfg.Gateway); err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())
	server.mu.Lock()
	instance := server.httpServer
	requestCtx := instance.BaseContext(server.listener)
	listener := server.listener
	server.mu.Unlock()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("unexpected Serve exit did not cancel instance context")
	}
	// Start 与退出回调通过同一锁串行，回调不能清空重启后的实例。
	if err := server.Start(cfg.Gateway); err != nil {
		t.Fatal(err)
	}
	if !server.Running() || server.LastError() != "" {
		t.Fatal("restart failed")
	}
}

func TestGatewaySuccessfulAndRepeatedStopClearError(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "running"}[running], func(t *testing.T) {
			cfg, _ := gatewayTestConfig(t, "secret-token")
			cfg.Gateway.ListenAddr = "127.0.0.1:0"
			server := New(&fakeStreamer{}, staticConfig{cfg: cfg})
			defer server.Stop(context.Background())
			if running {
				if err := server.Start(cfg.Gateway); err != nil {
					t.Fatal(err)
				}
			}
			occupied, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer occupied.Close()
			cfg.Gateway.ListenAddr = occupied.Addr().String()
			if err := server.Start(cfg.Gateway); err == nil {
				t.Fatal("expected bind error")
			}
			if err := server.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if server.LastError() != "" {
				t.Fatalf("successful stop kept stale error: %s", server.LastError())
			}
			if err := server.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGatewayConcurrentStartStop(t *testing.T) {
	cfg, _ := gatewayTestConfig(t, "secret-token")
	cfg.Gateway.ListenAddr = "127.0.0.1:0"
	server := New(&fakeStreamer{}, staticConfig{cfg: cfg})
	defer server.Stop(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if err := server.Start(cfg.Gateway); err != nil {
					t.Error(err)
				}
				if err := server.Stop(context.Background()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if err := server.Start(cfg.Gateway); err != nil {
		t.Fatal(err)
	}
	if !server.Running() || server.LastError() != "" {
		t.Fatal("final restart failed")
	}
}

func TestGatewayStopTimeoutCancelsRequest(t *testing.T) {
	for _, path := range []string{chatCompletionsPath, responsesPath} {
		t.Run(path, func(t *testing.T) {
			cfg, _ := gatewayTestConfig(t, "secret-token")
			cfg.Gateway.ListenAddr = "127.0.0.1:0"
			started := make(chan context.Context, 1)
			canceled := make(chan struct{})
			release := make(chan struct{})
			fake := &fakeStreamer{start: func(ctx context.Context, _ forwarder.ProviderRequest, _ func(modeladapter.ModelEvent) error) error {
				started <- ctx
				select {
				case <-ctx.Done():
					close(canceled)
					return ctx.Err()
				case <-release:
					return nil
				}
			}}
			server := New(fake, staticConfig{cfg: cfg})
			if err := server.Start(cfg.Gateway); err != nil {
				t.Fatal(err)
			}
			defer func() { close(release); _ = server.Stop(context.Background()) }()
			body := `{"model":"public-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`
			if path == responsesPath {
				body = `{"model":"public-a","stream":true,"input":"hi"}`
			}
			req, _ := http.NewRequest(http.MethodPost, "http://"+server.ListenAddr()+path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer secret-token")
			req.Header.Set("Content-Type", "application/json")
			transport := &http.Transport{Proxy: nil}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			awaitLifecycle(t, started)
			readDone := make(chan error, 1)
			go func() { _, err := io.Copy(io.Discard, response.Body); readDone <- err }()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if err := server.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Stop error=%v", err)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("Stop timeout did not cancel provider")
			}
			awaitLifecycle(t, readDone)
			if server.Running() || server.ListenAddr() != "" || server.LastError() == "" {
				t.Fatalf("invalid stopped state: running=%v addr=%q error=%q", server.Running(), server.ListenAddr(), server.LastError())
			}
		})
	}
}
