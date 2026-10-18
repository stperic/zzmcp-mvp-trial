package zzmcp

import (
	"context"
	"io"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServeStdio serves server over standard input and output until the client
// disconnects or ctx ends. maxLineBytes bounds one inbound JSON-RPC frame;
// zero selects the SDK default.
//
// The SDK handles tool calls concurrently and cancels calls still running
// when input reaches end of file, so clients must wait for a result before
// sending a call that depends on it and keep input open until then.
func ServeStdio(ctx context.Context, server *mcp.Server, maxLineBytes int) error {
	return server.Run(ctx, &mcp.StdioTransport{MaxLineLength: maxLineBytes})
}

// ServeIO serves server over arbitrary streams, such as pipes, with the same
// behavior as ServeStdio.
func ServeIO(ctx context.Context, server *mcp.Server, input io.Reader, output io.Writer, maxLineBytes int) error {
	return server.Run(ctx, &mcp.IOTransport{Reader: io.NopCloser(input), Writer: nopWriteCloser{output},
		MaxLineLength: maxLineBytes})
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// HTTPOptions configures HTTPHandler.
type HTTPOptions struct {
	// MaxRequestBytes bounds one request body. Zero selects the SDK default.
	MaxRequestBytes int64
}

// HTTPHandler serves server over stateless streamable HTTP with JSON
// responses, cross-origin protection and request cancellation. Tool handlers
// read the HTTP request that carried their call with HTTPRequestFromContext.
func HTTPHandler(server *mcp.Server, opts HTTPOptions) http.Handler {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, CrossOriginProtection: http.NewCrossOriginProtection(),
		MaxRequestBodyBytes: opts.MaxRequestBytes, PropagateRequestCancellation: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := HTTPRequestInfo{Header: r.Header.Clone(), RemoteAddr: r.RemoteAddr}
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), httpRequestKey{}, info)))
	})
}

// HTTPRequestInfo describes the HTTP request that carried a tool call.
type HTTPRequestInfo struct {
	// Header holds the request's header fields as they reached HTTPHandler,
	// credentials included. Host is not among them; net/http keeps it apart.
	Header http.Header
	// RemoteAddr is the request's RemoteAddr as it reached HTTPHandler,
	// usually the peer's "IP:port". Forwarding headers such as
	// X-Forwarded-For are not applied to it.
	RemoteAddr string
}

type httpRequestKey struct{}

// HTTPRequestFromContext returns the HTTP request that carried the tool call
// whose context is ctx, with a header the caller may modify. It reports false
// for calls that did not arrive through HTTPHandler.
//
// This package does not authenticate callers: the header carries whatever
// credentials the client sent, so keep it out of logs.
func HTTPRequestFromContext(ctx context.Context) (HTTPRequestInfo, bool) {
	info, ok := ctx.Value(httpRequestKey{}).(HTTPRequestInfo)
	if !ok {
		return HTTPRequestInfo{}, false
	}
	info.Header = info.Header.Clone()
	return info, true
}
