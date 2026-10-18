# zzmcp

Small MCP conventions shared by the zz Go projects, on top of the official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk). Each project keeps its own tool dispatch; this module only standardizes how tools are declared, validated, answered and served.

## What it provides

- **One error shape.** A tool error result carries a body as `structuredContent` and the error's text as text. `zzmcp.Error` is the default: `{"code", "message", "details"}` with `CODE: message` as text. Any error with a `StructuredContent() any` method supplies its own body, for example an RFC 9457 problem, and keeps its own text. Plain errors report `TOOL_FAILED`, with the error text as both message and text.
- **Schema-validated tools.** `AddSchemaTool` takes a hand-written JSON Schema and a raw-argument handler. `AddTool[In, Out]` infers the schema from a Go struct. Both validate arguments before the handler runs and report failures as `INVALID_ARGUMENTS`. A server built with `NewServer` can report its own error instead, for example an RFC 9457 problem: `Options.InvalidArguments` receives the default error and returns the one to report, or nil to keep the default. Errors a handler returns, and tools registered directly with the SDK, are not affected.
- **Structured object results.** Results must be JSON objects; anything else reports `INVALID_RESULT`. A result becomes `structuredContent`, with the same JSON as text. For a tool registered with `AddTool` or `AddSchemaTool`, a valid object output that implements `Summary() string` supplies the text instead when the summary is non-empty. An empty summary keeps the JSON text. Invalid output is rejected before its summary is called.
- **Client-friendly schemas.** `SchemaFor[T]` rewrites boolean subschemas as equivalent objects, because some clients reject them.
- **Serving.** `NewServer` sets instructions, an optional `tools/list` cache hint and the error middleware. `ServeStdio`, `ServeIO` and `HTTPHandler` (stateless streamable HTTP with JSON responses) cover the transports in use.
- **HTTP request access.** A tool handler serving a call that arrived through `HTTPHandler` reads that request's header and remote address with `HTTPRequestFromContext(ctx)`, for example to forward an API key. Each call gets its own copy of the header. The header includes credentials unfiltered, so keep it out of logs. `RemoteAddr` is the request's own, not derived from `X-Forwarded-For`. Over other transports it reports false.

Unknown tool names stay JSON-RPC protocol errors (`-32602`), as the MCP specification requires.

## Behavior to know

The SDK handles tool calls concurrently. A client must wait for a result before sending a call that depends on it. Over stdio, the SDK cancels calls still running when input reaches end of file, so keep input open until the last response arrives. The SDK's own `mcp.CommandTransport` and `mcp.IOTransport` clients do this.

## Status

- Public MVP qualification snapshot of the local module. The maintained module has not been released. Consumers use a `replace github.com/stperic/zzmcp => ../zzmcp` directive (adjust the relative path).
- First consumer: zztrace. zzrouter, AlphaDB and zzfuse can migrate later; each currently has its own copy of these helpers on SDK v1.3.0 to v1.8.0.
- License: unselected for this qualification snapshot.

## Development

```sh
go vet ./...
go test -race ./...
```
