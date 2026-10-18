package zzmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Handler serves one call with arguments already validated against the
// tool's input schema. It returns a value that marshals to a JSON object;
// a json.RawMessage is used as is.
type Handler func(ctx context.Context, arguments json.RawMessage) (any, error)

// Summarizer lets a typed tool's output supply the text content shown to the
// model. Without it, the text content is the JSON result.
type Summarizer interface {
	Summary() string
}

// AddSchemaTool registers a tool whose input schema is written by hand, as a
// JSON-compatible map or a *jsonschema.Schema with type "object".
func AddSchemaTool(server *mcp.Server, tool *mcp.Tool, handler Handler) error {
	return addTool(server, tool, func(ctx context.Context, arguments json.RawMessage) (any, Summarizer, error) {
		output, err := handler(ctx, arguments)
		return output, nil, err
	})
}

// AddTool registers a typed tool. Without an explicit input schema, the
// schema is inferred from In with SchemaFor.
func AddTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handler func(context.Context, In) (Out, error)) error {
	registered := *tool
	if registered.InputSchema == nil {
		schema, err := SchemaFor[In](nil)
		if err != nil {
			return fmt.Errorf("tool %s: %w", tool.Name, err)
		}
		registered.InputSchema = schema
	}
	return addTool(server, &registered, func(ctx context.Context, arguments json.RawMessage) (any, Summarizer, error) {
		var input In
		if err := json.Unmarshal(arguments, &input); err != nil {
			return nil, nil, invalidArguments(ctx, Errorf(CodeInvalidArguments, "%v", err))
		}
		output, err := handler(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		summary, _ := any(output).(Summarizer)
		return output, summary, nil
	})
}

func addTool(server *mcp.Server, tool *mcp.Tool, call func(context.Context, json.RawMessage) (any, Summarizer, error)) error {
	resolved, err := resolveInputSchema(tool.InputSchema)
	if err != nil {
		return fmt.Errorf("tool %s: %w", tool.Name, err)
	}
	registered := *tool
	server.AddTool(&registered, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		arguments, invalid := validArguments(resolved, req.Params.Arguments)
		if invalid != nil {
			return ErrorResult(invalidArguments(ctx, invalid)), nil
		}
		output, summary, err := call(ctx, arguments)
		if err != nil {
			return ErrorResult(err), nil
		}
		result := ObjectResult(output, "")
		if !result.IsError && summary != nil {
			if text := summary.Summary(); text != "" {
				result.Content = []mcp.Content{&mcp.TextContent{Text: text}}
			}
		}
		return result, nil
	})
	return nil
}

// ObjectResult returns a successful tool result whose structuredContent is
// output and whose text content is text, or the JSON result when text is
// empty. A value that is not a JSON object yields a CodeInvalidResult error.
func ObjectResult(output any, text string) *mcp.CallToolResult {
	raw, ok := output.(json.RawMessage)
	if !ok {
		var err error
		if raw, err = json.Marshal(output); err != nil {
			return ErrorResult(Errorf(CodeInvalidResult, "tool result cannot be encoded: %v", err))
		}
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || !json.Valid(raw) {
		return ErrorResult(NewError(CodeInvalidResult, "tool result is not a JSON object"))
	}
	if text == "" {
		text = string(raw)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: raw}
}

// SchemaFor infers an input schema for T. Boolean subschemas are rewritten as
// equivalent objects ({} and {"not": {}}) because some clients reject them.
// json.RawMessage fields accept any JSON value unless types overrides them.
func SchemaFor[T any](types map[reflect.Type]*jsonschema.Schema) (map[string]any, error) {
	overrides := map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): {}}
	for kind, schema := range types {
		overrides[kind] = schema
	}
	schema, err := jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: overrides})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	object, ok := objectSchemas(value).(map[string]any)
	if !ok {
		return nil, errors.New("inferred schema is not an object")
	}
	return object, nil
}

func objectSchemas(value any) any {
	if boolean, ok := value.(bool); ok {
		if boolean {
			return map[string]any{}
		}
		return map[string]any{"not": map[string]any{}}
	}
	schema, ok := value.(map[string]any)
	if !ok {
		return value
	}
	for key, child := range schema {
		switch key {
		case "properties", "$defs", "definitions", "patternProperties", "dependentSchemas":
			children, _ := child.(map[string]any)
			for name, sub := range children {
				children[name] = objectSchemas(sub)
			}
		case "items", "additionalProperties", "additionalItems", "unevaluatedProperties", "unevaluatedItems",
			"propertyNames", "contains", "not", "if", "then", "else", "contentSchema":
			schema[key] = objectSchemas(child)
		case "allOf", "anyOf", "oneOf", "prefixItems":
			children, _ := child.([]any)
			for i, sub := range children {
				children[i] = objectSchemas(sub)
			}
		}
	}
	return schema
}

func resolveInputSchema(schema any) (*jsonschema.Resolved, error) {
	if schema == nil {
		return nil, errors.New("input schema is required")
	}
	parsed, ok := schema.(*jsonschema.Schema)
	if !ok {
		raw, err := json.Marshal(schema)
		if err != nil {
			return nil, fmt.Errorf("input schema cannot be encoded: %w", err)
		}
		parsed = new(jsonschema.Schema)
		if err := json.Unmarshal(raw, parsed); err != nil {
			return nil, fmt.Errorf("input schema is invalid: %w", err)
		}
	}
	if parsed == nil || parsed.Type != "object" {
		return nil, errors.New(`input schema must have type "object"`)
	}
	return parsed.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
}

type invalidArgumentsKey struct{}

// invalidArguments returns the error reported for arguments rejected with err:
// the one supplied by the server's Options.InvalidArguments, or err itself.
func invalidArguments(ctx context.Context, err *Error) error {
	if supply, ok := ctx.Value(invalidArgumentsKey{}).(func(*Error) error); ok {
		if supplied := supply(err); supplied != nil {
			return supplied
		}
	}
	return err
}

func validArguments(resolved *jsonschema.Resolved, raw json.RawMessage) (json.RawMessage, *Error) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		raw = json.RawMessage(`{}`)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		return nil, Errorf(CodeInvalidArguments, "arguments are not valid JSON: %v", err)
	}
	if _, ok := instance.(map[string]any); !ok {
		return nil, NewError(CodeInvalidArguments, "arguments must be a JSON object")
	}
	if err := resolved.Validate(instance); err != nil {
		return nil, Errorf(CodeInvalidArguments, "%v", err)
	}
	return raw, nil
}
