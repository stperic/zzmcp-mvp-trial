package zzmcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Codes reported by this package. Projects define their own codes for
// domain failures.
const (
	CodeInvalidArguments = "INVALID_ARGUMENTS"
	CodeInvalidResult    = "INVALID_RESULT"
	CodeToolFailed       = "TOOL_FAILED"
)

// Error is a tool failure with a stable, machine-readable code. A tool error
// result carries it as structuredContent and its Error text as content.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// StructuredContent returns the error body for a tool result.
func (e *Error) StructuredContent() any { return e }

// NewError returns a coded tool error.
func NewError(code, message string) *Error { return &Error{Code: code, Message: message} }

// Errorf returns a coded tool error with a formatted message.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// StructuredError is an error that supplies its own structuredContent body,
// for example an RFC 9457 problem.
type StructuredError interface {
	error
	StructuredContent() any
}

// ErrorResult converts err into a tool error result. An error without its own
// body reports CodeToolFailed.
func ErrorResult(err error) *mcp.CallToolResult {
	var structured StructuredError
	if !errors.As(err, &structured) {
		structured = NewError(CodeToolFailed, err.Error())
	}
	result := &mcp.CallToolResult{StructuredContent: structured.StructuredContent()}
	result.SetError(err)
	return result
}

// ErrorMiddleware gives tool errors raised through the SDK's own typed tools
// the same structuredContent body as tools registered by this package.
func ErrorMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, req)
		if call, ok := result.(*mcp.CallToolResult); ok && call != nil && call.IsError && call.StructuredContent == nil {
			var structured StructuredError
			if errors.As(call.GetError(), &structured) {
				call.StructuredContent = structured.StructuredContent()
			}
		}
		return result, err
	}
}
