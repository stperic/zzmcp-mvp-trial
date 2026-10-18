package zzmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := t.Context()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, session *mcp.ClientSession, name string, arguments any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result
}

// structured decodes a result's structuredContent the way a remote client sees it.
func structured(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("structuredContent is not an object: %s", raw)
	}
	return value
}

func text(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("expected one content block, got %d", len(result.Content))
	}
	block, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, not text", result.Content[0])
	}
	return block.Text
}

var echoSchema = map[string]any{
	"type": "object", "additionalProperties": false, "required": []string{"name"},
	"properties": map[string]any{
		"name":  map[string]any{"type": "string", "minLength": 1},
		"count": map[string]any{"type": "integer", "minimum": 0},
	},
}

func echoServer(t *testing.T, calls *atomic.Int32, handler Handler) *mcp.ClientSession {
	t.Helper()
	server := NewServer(Options{Name: "test", Version: "1", Instructions: "Use echo."})
	if handler == nil {
		handler = func(_ context.Context, arguments json.RawMessage) (any, error) {
			calls.Add(1)
			return arguments, nil
		}
	}
	if err := AddSchemaTool(server, &mcp.Tool{Name: "echo", Description: "Echo arguments.", InputSchema: echoSchema}, handler); err != nil {
		t.Fatal(err)
	}
	return connect(t, server)
}

func TestSchemaToolReturnsStructuredObject(t *testing.T) {
	var calls atomic.Int32
	session := echoServer(t, &calls, nil)
	if got := session.InitializeResult().Instructions; got != "Use echo." {
		t.Fatalf("instructions were not sent: %q", got)
	}
	result := call(t, session, "echo", map[string]any{"name": "a", "count": 2})
	if result.IsError || structured(t, result)["name"] != "a" || !strings.Contains(text(t, result), `"count":2`) {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestSchemaToolRejectsInvalidArgumentsBeforeHandler(t *testing.T) {
	var calls atomic.Int32
	session := echoServer(t, &calls, nil)
	for _, arguments := range []map[string]any{
		{"name": "a", "extra": true},
		{"count": 1},
		{"name": ""},
		{"name": "a", "count": -1},
		{"name": "a", "count": "two"},
	} {
		result := call(t, session, "echo", arguments)
		if !result.IsError || structured(t, result)["code"] != CodeInvalidArguments ||
			!strings.HasPrefix(text(t, result), CodeInvalidArguments+": ") {
			t.Fatalf("arguments %v were not rejected as invalid: %+v", arguments, result)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("handler ran for invalid arguments %d times", calls.Load())
	}
}

func TestToolErrorsCarryCodes(t *testing.T) {
	coded := echoServer(t, nil, func(context.Context, json.RawMessage) (any, error) {
		return nil, NewError("FORBIDDEN", "operation requires the owner")
	})
	result := call(t, coded, "echo", map[string]any{"name": "a"})
	body := structured(t, result)
	if !result.IsError || body["code"] != "FORBIDDEN" || body["message"] != "operation requires the owner" ||
		text(t, result) != "FORBIDDEN: operation requires the owner" {
		t.Fatalf("coded error lost its code: %+v", result)
	}
	plain := echoServer(t, nil, func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New("disk unavailable")
	})
	result = call(t, plain, "echo", map[string]any{"name": "a"})
	if !result.IsError || structured(t, result)["code"] != CodeToolFailed || text(t, result) != "disk unavailable" {
		t.Fatalf("plain error was not reported as a tool failure: %+v", result)
	}
}

func TestNonObjectResultIsRejected(t *testing.T) {
	for _, output := range []any{json.RawMessage(`[1]`), json.RawMessage(`{"open":`), "text", func() {}} {
		session := echoServer(t, nil, func(context.Context, json.RawMessage) (any, error) { return output, nil })
		result := call(t, session, "echo", map[string]any{"name": "a"})
		if !result.IsError || structured(t, result)["code"] != CodeInvalidResult {
			t.Fatalf("result %T was accepted: %+v", output, result)
		}
	}
}

func TestUnknownToolIsProtocolError(t *testing.T) {
	session := echoServer(t, nil, nil)
	_, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "missing", Arguments: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("unknown tool did not fail at the protocol level: %v", err)
	}
}

type greetInput struct {
	Name    string          `json:"name" jsonschema:"Person to greet."`
	Options json.RawMessage `json:"options,omitempty"`
}

type greetOutput struct {
	Greeting string `json:"greeting"`
}

func (g greetOutput) Summary() string { return "Greeted: " + g.Greeting }

func TestTypedToolInfersObjectSchemaAndSummary(t *testing.T) {
	server := NewServer(Options{Name: "test", Version: "1", ToolsListTTL: 5 * time.Minute})
	err := AddTool(server, &mcp.Tool{Name: "greet", Description: "Greet someone."},
		func(_ context.Context, in greetInput) (greetOutput, error) {
			return greetOutput{Greeting: "hello " + in.Name}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	session := connect(t, server)
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil || len(listed.Tools) != 1 {
		t.Fatalf("list tools: %+v, %v", listed, err)
	}
	if listed.TTLMs != int((5 * time.Minute).Milliseconds()) {
		t.Fatalf("tools/list cache hint is %d ms", listed.TTLMs)
	}
	schema, err := json.Marshal(listed.Tools[0].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(schema), ":false") || strings.Contains(string(schema), ":true") ||
		!strings.Contains(string(schema), `"Person to greet."`) {
		t.Fatalf("schema kept boolean subschemas or lost descriptions: %s", schema)
	}
	result := call(t, session, "greet", map[string]any{"name": "Ada", "options": []int{1}})
	if result.IsError || text(t, result) != "Greeted: hello Ada" || structured(t, result)["greeting"] != "hello Ada" {
		t.Fatalf("unexpected typed result: %+v", result)
	}
	if result := call(t, session, "greet", map[string]any{"name": "Ada", "other": 1}); !result.IsError ||
		structured(t, result)["code"] != CodeInvalidArguments {
		t.Fatalf("typed tool accepted an unknown property: %+v", result)
	}
}

func TestErrorMiddlewareCoversSDKTypedTools(t *testing.T) {
	server := NewServer(Options{Name: "test", Version: "1"})
	mcp.AddTool(server, &mcp.Tool{Name: "fail"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return nil, nil, NewError("NOT_FOUND", "no such record")
	})
	result := call(t, connect(t, server), "fail", map[string]any{})
	if !result.IsError || structured(t, result)["code"] != "NOT_FOUND" {
		t.Fatalf("SDK typed tool error lost its structured body: %+v", result)
	}
}

type countInput struct {
	Count int `json:"count"`
}

// rejectedCalls are rejected before the handler runs, one per path: a schema
// violation and a non-object for AddSchemaTool, a schema violation for AddTool,
// and a decode failure for AddTool under a looser explicit schema.
var rejectedCalls = []struct {
	tool      string
	arguments any
}{
	{"echo", map[string]any{"name": "a", "count": -1}},
	{"echo", []int{1}},
	{"greet", map[string]any{"name": "Ada", "other": 1}},
	{"count", map[string]any{"count": "x"}},
}

// addRejectingTools registers the tools named in rejectedCalls, each counting
// its handler runs in calls.
func addRejectingTools(t *testing.T, server *mcp.Server, calls *atomic.Int32) {
	t.Helper()
	if err := AddSchemaTool(server, &mcp.Tool{Name: "echo", InputSchema: echoSchema},
		func(_ context.Context, arguments json.RawMessage) (any, error) {
			calls.Add(1)
			return arguments, nil
		}); err != nil {
		t.Fatal(err)
	}
	if err := AddTool(server, &mcp.Tool{Name: "greet"}, func(_ context.Context, in greetInput) (greetOutput, error) {
		calls.Add(1)
		return greetOutput{Greeting: "hello " + in.Name}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := AddTool(server, &mcp.Tool{Name: "count", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, in countInput) (countInput, error) {
			calls.Add(1)
			return in, nil
		}); err != nil {
		t.Fatal(err)
	}
}

// problemError is an RFC 9457 problem with an extension code, the shape
// zzrouter reports for invalid input.
type problemError struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
}

func (p *problemError) Error() string          { return p.Detail }
func (p *problemError) StructuredContent() any { return p }

func TestInvalidArgumentsUseServerError(t *testing.T) {
	var mu sync.Mutex
	var received []*Error
	server := NewServer(Options{Name: "test", Version: "1", InvalidArguments: func(err *Error) error {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, err)
		return &problemError{Type: "about:blank", Title: "Bad Request", Status: http.StatusBadRequest,
			Detail: "invalid input: " + err.Message, Code: "mcp_invalid_input"}
	}})
	var calls atomic.Int32
	addRejectingTools(t, server, &calls)
	session := connect(t, server)
	for i, rejected := range rejectedCalls {
		result := call(t, session, rejected.tool, rejected.arguments)
		mu.Lock()
		if len(received) != i+1 {
			t.Fatalf("%s %v: InvalidArguments ran %d times", rejected.tool, rejected.arguments, len(received))
		}
		got := received[i]
		mu.Unlock()
		if got.Code != CodeInvalidArguments || got.Message == "" {
			t.Fatalf("%s %v: InvalidArguments received %+v", rejected.tool, rejected.arguments, got)
		}
		want := map[string]any{"type": "about:blank", "title": "Bad Request", "status": float64(http.StatusBadRequest),
			"detail": "invalid input: " + got.Message, "code": "mcp_invalid_input"}
		if !result.IsError || !reflect.DeepEqual(structured(t, result), want) || text(t, result) != want["detail"] {
			t.Fatalf("%s %v: server error was not reported: %+v", rejected.tool, rejected.arguments, result)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("handlers ran for invalid arguments %d times", calls.Load())
	}
	if result := call(t, session, "echo", map[string]any{"name": "a"}); result.IsError || calls.Load() != 1 {
		t.Fatalf("valid call failed: %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != len(rejectedCalls) {
		t.Fatalf("InvalidArguments ran for a valid call")
	}
}

func TestInvalidArgumentsDefaultToCode(t *testing.T) {
	var declined atomic.Int32
	for _, tc := range []struct {
		name   string
		server *mcp.Server
	}{
		{"NewServer without InvalidArguments", NewServer(Options{Name: "test", Version: "1"})},
		{"InvalidArguments returning nil", NewServer(Options{Name: "test", Version: "1",
			InvalidArguments: func(*Error) error {
				declined.Add(1)
				return nil
			}})},
		{"SDK server", mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)},
	} {
		var calls atomic.Int32
		addRejectingTools(t, tc.server, &calls)
		session := connect(t, tc.server)
		for _, rejected := range rejectedCalls {
			result := call(t, session, rejected.tool, rejected.arguments)
			if !result.IsError || structured(t, result)["code"] != CodeInvalidArguments ||
				!strings.HasPrefix(text(t, result), CodeInvalidArguments+": ") {
				t.Fatalf("%s: %s %v did not report %s: %+v", tc.name, rejected.tool, rejected.arguments,
					CodeInvalidArguments, result)
			}
		}
		if calls.Load() != 0 {
			t.Fatalf("%s: handlers ran for invalid arguments %d times", tc.name, calls.Load())
		}
	}
	if declined.Load() != int32(len(rejectedCalls)) {
		t.Fatalf("InvalidArguments returning nil ran %d times, want %d", declined.Load(), len(rejectedCalls))
	}
}

type nameInput struct {
	Name string `json:"name"`
}

func TestInvalidArgumentsSkipOtherErrors(t *testing.T) {
	var supplied atomic.Int32
	server := NewServer(Options{Name: "test", Version: "1", InvalidArguments: func(err *Error) error {
		supplied.Add(1)
		return &problemError{Title: "Bad Request", Status: http.StatusBadRequest, Detail: err.Message, Code: "mcp_invalid_input"}
	}})
	if err := AddSchemaTool(server, &mcp.Tool{Name: "taken", InputSchema: echoSchema},
		func(context.Context, json.RawMessage) (any, error) {
			return nil, Errorf(CodeInvalidArguments, "name %q is taken", "a")
		}); err != nil {
		t.Fatal(err)
	}
	if err := AddTool(server, &mcp.Tool{Name: "typed-taken"}, func(context.Context, countInput) (countInput, error) {
		return countInput{}, NewError(CodeInvalidArguments, "count is taken")
	}); err != nil {
		t.Fatal(err)
	}
	var sdkCalls atomic.Int32
	mcp.AddTool(server, &mcp.Tool{Name: "sdk-greet"},
		func(context.Context, *mcp.CallToolRequest, nameInput) (*mcp.CallToolResult, any, error) {
			sdkCalls.Add(1)
			return nil, nil, nil
		})
	session := connect(t, server)
	for _, handled := range []struct {
		tool      string
		arguments map[string]any
		text      string
	}{
		{"taken", map[string]any{"name": "a"}, `INVALID_ARGUMENTS: name "a" is taken`},
		{"typed-taken", map[string]any{"count": 1}, "INVALID_ARGUMENTS: count is taken"},
	} {
		result := call(t, session, handled.tool, handled.arguments)
		if !result.IsError || structured(t, result)["code"] != CodeInvalidArguments || text(t, result) != handled.text {
			t.Fatalf("%s: handler error was not reported unchanged: %+v", handled.tool, result)
		}
	}
	result := call(t, session, "sdk-greet", map[string]any{"name": 5})
	if !result.IsError || sdkCalls.Load() != 0 || structured(t, result)["code"] == "mcp_invalid_input" {
		t.Fatalf("SDK tool did not keep the SDK's own validation: %+v", result)
	}
	if supplied.Load() != 0 {
		t.Fatalf("InvalidArguments ran %d times for errors zzmcp did not raise", supplied.Load())
	}
}

func TestServeIOAnswersSequentialCalls(t *testing.T) {
	server := NewServer(Options{Name: "test", Version: "1"})
	if err := AddSchemaTool(server, &mcp.Tool{Name: "echo", InputSchema: echoSchema},
		func(_ context.Context, arguments json.RawMessage) (any, error) { return arguments, nil }); err != nil {
		t.Fatal(err)
	}
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	served := make(chan error, 1)
	go func() {
		served <- ServeIO(t.Context(), server, serverIn, serverOut, 0)
		_ = serverOut.Close()
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "pipe-client", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if result := call(t, session, "echo", map[string]any{"name": name}); result.IsError || structured(t, result)["name"] != name {
			t.Fatalf("call %s over IO failed: %+v", name, result)
		}
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-served; err != nil {
		t.Fatalf("ServeIO did not end cleanly: %v", err)
	}
}

func TestHTTPHandlerServesStatelessCalls(t *testing.T) {
	server := NewServer(Options{Name: "test", Version: "1"})
	if err := AddSchemaTool(server, &mcp.Tool{Name: "echo", InputSchema: echoSchema},
		func(_ context.Context, arguments json.RawMessage) (any, error) { return arguments, nil }); err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(HTTPHandler(server, HTTPOptions{MaxRequestBytes: 1 << 20}))
	defer endpoint.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "http-client", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if result := call(t, session, "echo", map[string]any{"name": "web"}); result.IsError || structured(t, result)["name"] != "web" {
		t.Fatalf("HTTP call failed: %+v", result)
	}
}

func TestSchemaToolRequiresObjectSchema(t *testing.T) {
	server := NewServer(Options{Name: "test", Version: "1"})
	handler := func(context.Context, json.RawMessage) (any, error) { return map[string]any{}, nil }
	for _, schema := range []any{nil, map[string]any{"type": "array"}, map[string]any{"type": "object", "minProperties": "x"}} {
		if err := AddSchemaTool(server, &mcp.Tool{Name: "bad", InputSchema: schema}, handler); err == nil {
			t.Fatalf("schema %v was accepted", schema)
		}
	}
}

type requestReport struct {
	OK         bool        `json:"ok"`
	DerivedOK  bool        `json:"derivedOk"`
	Header     http.Header `json:"header,omitempty"`
	RemoteAddr string      `json:"remoteAddr"`
}

func reportRequest(ctx context.Context) requestReport {
	info, ok := HTTPRequestFromContext(ctx)
	derived, cancel := context.WithCancel(ctx)
	defer cancel()
	_, derivedOK := HTTPRequestFromContext(derived)
	return requestReport{OK: ok, DerivedOK: derivedOK, Header: info.Header, RemoteAddr: info.RemoteAddr}
}

var whoamiTools = []string{"schema-whoami", "typed-whoami", "sdk-whoami"}

// whoamiServer registers one request-reporting tool per registration path.
func whoamiServer(t *testing.T) *mcp.Server {
	t.Helper()
	server := NewServer(Options{Name: "test", Version: "1"})
	if err := AddSchemaTool(server, &mcp.Tool{Name: "schema-whoami", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, _ json.RawMessage) (any, error) { return reportRequest(ctx), nil }); err != nil {
		t.Fatal(err)
	}
	if err := AddTool(server, &mcp.Tool{Name: "typed-whoami"},
		func(ctx context.Context, _ struct{}) (requestReport, error) { return reportRequest(ctx), nil }); err != nil {
		t.Fatal(err)
	}
	mcp.AddTool(server, &mcp.Tool{Name: "sdk-whoami"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, requestReport, error) {
			return nil, reportRequest(ctx), nil
		})
	return server
}

func decodeReport(t *testing.T, result *mcp.CallToolResult) requestReport {
	t.Helper()
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var report requestReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("result is not a request report: %s", raw)
	}
	return report
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// httpSession connects to endpoint with setHeader applied to every request.
func httpSession(t *testing.T, endpoint string, setHeader func(http.Header)) *mcp.ClientSession {
	t.Helper()
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		setHeader(r.Header)
		return http.DefaultTransport.RoundTrip(r)
	})
	client := mcp.NewClient(&mcp.Implementation{Name: "http-client", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint,
		HTTPClient: &http.Client{Transport: transport}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestHTTPRequestReachesToolHandlers(t *testing.T) {
	inner := HTTPHandler(whoamiServer(t), HTTPOptions{})
	var mu sync.Mutex
	received := map[string]HTTPRequestInfo{}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received[r.Header.Get("X-Request-Seq")] = HTTPRequestInfo{Header: r.Header.Clone(), RemoteAddr: r.RemoteAddr}
		mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(endpoint.Close)
	var seq atomic.Int32
	session := httpSession(t, endpoint.URL, func(h http.Header) {
		h.Set("X-Request-Seq", strconv.Itoa(int(seq.Add(1))))
		h.Set("Authorization", "Bearer secret-token")
		h.Set("X-Api-Key", "key-123")
		h.Set("X-Forwarded-For", "203.0.113.9")
		h.Set("Forwarded", "for=203.0.113.9")
		h["X-Multi"] = []string{"one", "two"}
	})
	for _, name := range whoamiTools {
		result := call(t, session, name, map[string]any{})
		report := decodeReport(t, result)
		mu.Lock()
		sent, found := received[report.Header.Get("X-Request-Seq")]
		mu.Unlock()
		if result.IsError || !report.OK || !report.DerivedOK || !found {
			t.Fatalf("%s did not see its HTTP request: %+v", name, report)
		}
		if !reflect.DeepEqual(report.Header, sent.Header) || report.Header.Get("Authorization") != "Bearer secret-token" ||
			report.Header.Get("X-Api-Key") != "key-123" || !slices.Equal(report.Header.Values("X-Multi"), []string{"one", "two"}) {
			t.Fatalf("%s saw header %v, request had %v", name, report.Header, sent.Header)
		}
		host, port, err := net.SplitHostPort(report.RemoteAddr)
		if report.RemoteAddr != sent.RemoteAddr || err != nil || !net.ParseIP(host).IsLoopback() || port == "" || port == "0" {
			t.Fatalf("%s saw remote address %q, request came from %q", name, report.RemoteAddr, sent.RemoteAddr)
		}
	}
}

// The SDK client negotiates the newest protocol, whose requests take a
// different SDK path from older ones, so older versions are posted by hand.
func TestHTTPRequestReachesToolHandlersOnOlderProtocols(t *testing.T) {
	endpoint := httptest.NewServer(HTTPHandler(whoamiServer(t), HTTPOptions{}))
	t.Cleanup(endpoint.Close)
	versions := slices.DeleteFunc(mcp.SupportedProtocolVersions(), func(v string) bool { return v >= "2026-07-28" })
	if len(versions) == 0 {
		t.Fatal("the SDK supports no protocol version older than 2026-07-28")
	}
	for _, version := range versions {
		for _, name := range whoamiTools {
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint.URL, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Mcp-Protocol-Version", version)
			req.Header.Set("X-Api-Key", "key-"+version)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var reply struct {
				Result struct{ StructuredContent requestReport }
				Error  json.RawMessage
			}
			err = json.NewDecoder(resp.Body).Decode(&reply)
			_ = resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK || reply.Error != nil {
				t.Fatalf("%s over %s: status %d, error %s, decode %v", name, version, resp.StatusCode, reply.Error, err)
			}
			report := reply.Result.StructuredContent
			host, _, err := net.SplitHostPort(report.RemoteAddr)
			if !report.OK || !report.DerivedOK || report.Header.Get("X-Api-Key") != "key-"+version ||
				err != nil || !net.ParseIP(host).IsLoopback() {
				t.Fatalf("%s over %s did not see its HTTP request: %+v", name, version, report)
			}
		}
	}
}

func TestHTTPRequestHeaderIsCopiedPerCall(t *testing.T) {
	server := NewServer(Options{Name: "test", Version: "1"})
	if err := AddSchemaTool(server, &mcp.Tool{Name: "mutate", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, _ json.RawMessage) (any, error) {
			first, _ := HTTPRequestFromContext(ctx)
			first.Header.Set("X-Api-Key", "changed")
			first.Header.Del("Authorization")
			first.Header.Add("X-Injected", "yes")
			second, _ := HTTPRequestFromContext(ctx)
			return map[string]any{"second": second.Header}, nil
		}); err != nil {
		t.Fatal(err)
	}
	inner := HTTPHandler(server, HTTPOptions{})
	var mu sync.Mutex
	var served []http.Header
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r)
		mu.Lock()
		served = append(served, r.Header.Clone())
		mu.Unlock()
	}))
	t.Cleanup(endpoint.Close)
	session := httpSession(t, endpoint.URL, func(h http.Header) {
		h.Set("Authorization", "Bearer secret-token")
		h.Set("X-Api-Key", "key-123")
	})
	raw, err := json.Marshal(call(t, session, "mutate", map[string]any{}).StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ Second http.Header }
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, header := range append(served, result.Second) {
		if header.Get("X-Api-Key") != "key-123" || header.Get("Authorization") != "Bearer secret-token" ||
			header.Get("X-Injected") != "" {
			t.Fatalf("a handler's header change leaked: %v", header)
		}
	}
}

func TestHTTPRequestAbsentOutsideHTTPHandler(t *testing.T) {
	if info, ok := HTTPRequestFromContext(context.Background()); ok || info.Header != nil || info.RemoteAddr != "" {
		t.Fatalf("a plain context reported an HTTP request: %+v", info)
	}
	server := whoamiServer(t)
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	go func() {
		_ = ServeIO(t.Context(), server, serverIn, serverOut, 0)
		_ = serverOut.Close()
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "pipe-client", Version: "1"}, nil)
	piped, err := client.Connect(t.Context(), &mcp.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = piped.Close() })
	for transport, session := range map[string]*mcp.ClientSession{"in-memory": connect(t, server), "ServeIO": piped} {
		for _, name := range whoamiTools {
			result := call(t, session, name, map[string]any{})
			if report := decodeReport(t, result); result.IsError || report.OK || report.DerivedOK ||
				report.Header != nil || report.RemoteAddr != "" {
				t.Fatalf("%s over %s reported an HTTP request: %+v", name, transport, report)
			}
		}
	}
}
