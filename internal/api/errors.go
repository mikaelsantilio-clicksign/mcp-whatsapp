package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/clicksign/whatsapp-mcp/internal/flow"
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
//
// The wire-level structs (InteractivePayload, FlowStateDigest, TraceStep,
// InteractiveReply) live in internal/flow so a Flow can populate a
// Result that gets serialised straight into the HTTP response.
type MessageResponse struct {
	Status       string                   `json:"status"`
	Reply        string                   `json:"reply"`
	AuthorizeURL string                   `json:"authorize_url,omitempty"`
	Interactive  *flow.InteractivePayload `json:"interactive,omitempty"`
	FlowState    *flow.FlowStateDigest    `json:"flow_state,omitempty"`
	Trace        []flow.TraceStep         `json:"trace,omitempty"`
	Error        *errorBody               `json:"error,omitempty"`
}
