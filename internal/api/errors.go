package api

import (
	"context"
	"encoding/json"
	"net/http"
)

func contextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxRequestID, id)
}

func RequestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxRequestID).(string)
	return v
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type errorBody struct {
	Code    string `json:"code"`
	Details string `json:"details,omitempty"`
}

// MessageResponse is the unified response shape of POST /api/messages.
type MessageResponse struct {
	Status       string          `json:"status"`
	Reply        string          `json:"reply"`
	AuthorizeURL string          `json:"authorize_url,omitempty"`
	ToolCalls    []ToolCallTrace `json:"tool_calls,omitempty"`
	Error        *errorBody      `json:"error,omitempty"`
}

type ToolCallTrace struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Err  string `json:"err,omitempty"`
}
