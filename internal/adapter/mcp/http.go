package mcp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPHandler serves server at /mcp for requests carrying the static bearer token.
// TLS is expected to be terminated by a reverse proxy on the same host.
// MCP requests are cancelled when ctx ends; the returned wait blocks until they have returned.
func HTTPHandler(ctx context.Context, server *mcp.Server, token string) (http.Handler, func()) {
	var calls sync.WaitGroup
	// The SDK detaches stateful-session handlers from HTTP request contexts,
	// so link them to the process lifecycle explicitly.
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(rctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			calls.Add(1)
			defer calls.Done()
			rctx, cancel := context.WithCancel(rctx)
			defer cancel()
			defer context.AfterFunc(ctx, cancel)()
			return next(rctx, method, req)
		}
	})
	want := sha256.Sum256([]byte(token))
	verify := func(_ context.Context, got string, _ *http.Request) (*auth.TokenInfo, error) {
		// Hash both sides so the comparison is constant-time regardless of length.
		if sum := sha256.Sum256([]byte(got)); subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{}, nil
	}
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{SessionTimeout: 30 * time.Minute})
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.NewCrossOriginProtection().Handler(
		auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(streamable)))
	return mux, calls.Wait
}
