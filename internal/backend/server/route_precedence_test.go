package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	serverconfig "cursor/internal/backend/server/config"
)

func TestExactRouteTakesPrecedenceOverWildcardRoute(t *testing.T) {
	const procedure = "/aiserver.v1.AiService/WriteGitCommitMessage"

	exactCalled := false
	wildcardCalled := false
	handler := New(
		POST(procedure,
			Name("exact_commit_message"),
			Local(func(ctx *Context) error {
				exactCalled = true
				ctx.Writer.WriteHeader(http.StatusNoContent)
				return nil
			}),
		),
		Any("/aiserver.v1.AiService/*",
			Name("ai_service_wildcard"),
			Local(func(ctx *Context) error {
				wildcardCalled = true
				ctx.Writer.WriteHeader(http.StatusAccepted)
				return nil
			}),
		),
	)

	request := httptest.NewRequest(http.MethodPost, procedure, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if !exactCalled {
		t.Fatal("exact route was not called")
	}
	if wildcardCalled {
		t.Fatal("wildcard route was called despite an exact match")
	}
}

func TestPolicySelectsLocalAndUpstreamActions(t *testing.T) {
	tests := []struct {
		name           string
		routingMode    string
		path           string
		upstreamTarget string
		wantAction     string
		wantStatus     int
	}{
		{
			name:        "local mode without upstream target",
			routingMode: "local",
			path:        "/fallback",
			wantAction:  "local",
			wantStatus:  http.StatusNoContent,
		},
		{
			name:        "upstream preference without upstream target stays local",
			routingMode: "upstream",
			path:        "/fallback",
			wantAction:  "local",
			wantStatus:  http.StatusNoContent,
		},
		{
			name:           "local preference with upstream target stays local",
			routingMode:    "local",
			path:           "/fallback",
			upstreamTarget: "https://api2.cursor.sh/fallback",
			wantAction:     "local",
			wantStatus:     http.StatusNoContent,
		},
		{
			name:           "upstream preference uses fallback",
			routingMode:    "upstream",
			path:           "/fallback",
			upstreamTarget: "https://api2.cursor.sh/fallback",
			wantAction:     "fallback",
			wantStatus:     http.StatusAccepted,
		},
		{
			name:           "explicit upstream action precedes fallback",
			routingMode:    "upstream",
			path:           "/explicit",
			upstreamTarget: "https://api2.cursor.sh/explicit",
			wantAction:     "explicit",
			wantStatus:     http.StatusCreated,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := serverconfig.NewStore(filepath.Join(root, "config.yaml"), filepath.Join(root, "logs"))
			manager, err := serverconfig.NewManager(context.Background(), store)
			if err != nil {
				t.Fatalf("create config manager: %v", err)
			}
			config := manager.Current()
			config.Routing.Mode = test.routingMode
			if _, err := manager.Save(context.Background(), config); err != nil {
				t.Fatalf("save routing mode: %v", err)
			}

			called := ""
			handler := New(
				Use(ServerContext(), PolicyMiddleware(manager)),
				FallbackUpstream(func(ctx *Context) error {
					called = "fallback"
					ctx.Writer.WriteHeader(http.StatusAccepted)
					return nil
				}),
				POST("/fallback",
					Name("fallback_route"),
					Local(func(ctx *Context) error {
						called = "local"
						ctx.Writer.WriteHeader(http.StatusNoContent)
						return nil
					}),
				),
				POST("/explicit",
					Name("explicit_route"),
					Local(func(ctx *Context) error {
						called = "local"
						ctx.Writer.WriteHeader(http.StatusNoContent)
						return nil
					}),
					Upstream(func(ctx *Context) error {
						called = "explicit"
						ctx.Writer.WriteHeader(http.StatusCreated)
						return nil
					}),
				),
			)

			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			if test.upstreamTarget != "" {
				request.Header.Set(HeaderServerUpstreamURL, test.upstreamTarget)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if called != test.wantAction {
				t.Fatalf("action = %q, want %q", called, test.wantAction)
			}
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}
