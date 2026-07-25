package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
