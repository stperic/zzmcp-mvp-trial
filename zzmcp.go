// Package zzmcp holds the small MCP conventions shared by zz projects on top
// of the official MCP Go SDK: one tool error shape, schema-validated tools
// with structured object results, and stdio and HTTP serving helpers.
//
// Each project keeps its own tool dispatch. This package does not route
// calls, authenticate callers or decide what a tool may do.
package zzmcp

import (
	"context"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configures a server built by NewServer.
type Options struct {
	Name    string
	Version string
	// Instructions are sent to clients during initialization.
	Instructions string
	// ToolsListTTL is the cache hint returned with tools/list. Zero sends none.
	ToolsListTTL time.Duration
	Logger       *slog.Logger
	// InvalidArguments, when set, supplies the error for a call to a tool
	// registered with AddSchemaTool or AddTool whose arguments are rejected
	// before its handler runs. It receives the default CodeInvalidArguments
	// error, whose Message describes the failure, and its result is reported
	// as a handler's error would be. Returning nil keeps the default.
	InvalidArguments func(*Error) error
}

// NewServer returns an SDK server with ErrorMiddleware installed.
func NewServer(opts Options) *mcp.Server {
	options := &mcp.ServerOptions{Instructions: opts.Instructions, Logger: opts.Logger}
	if opts.ToolsListTTL > 0 {
		ttl := int(opts.ToolsListTTL.Milliseconds())
		options.SetCacheable = func(_ context.Context, req mcp.Request, cache *mcp.Cacheable) {
			if _, ok := req.(*mcp.ListToolsRequest); ok {
				cache.TTLMs = ttl
			}
		}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: opts.Name, Version: opts.Version}, options)
	server.AddReceivingMiddleware(ErrorMiddleware)
	if supply := opts.InvalidArguments; supply != nil {
		server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				return next(context.WithValue(ctx, invalidArgumentsKey{}, supply), method, req)
			}
		})
	}
	return server
}
