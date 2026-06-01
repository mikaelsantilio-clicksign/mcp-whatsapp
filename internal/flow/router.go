package flow

import (
	"context"
	"errors"
	"log/slog"
)

// ErrUnknownIntent is returned when neither the NLU intent nor the active
// flow can be mapped to a registered Flow.
var ErrUnknownIntent = errors.New("flow: unknown intent")

// Router dispatches an Input to the appropriate Flow.
//
// Dispatch rules:
//
//  1. If session has an ActiveFlow, that flow handles the turn (regardless
//     of the NLU intent). Once a state machine is open, only it gets to
//     decide when to close.
//  2. Otherwise, look up the flow registered under the NLU intent.
//  3. On miss, return KindError with a friendly fallback reply.
//
// KindTransfer results are followed in-process up to maxTransfers hops to
// catch programming errors (loops). Trace from each hop is concatenated.
type Router struct {
	logger *slog.Logger
	flows  map[string]Flow
}

const maxTransfers = 3

// NewRouter wires Flow instances under their ID().
func NewRouter(logger *slog.Logger, flows ...Flow) *Router {
	m := make(map[string]Flow, len(flows))
	for _, f := range flows {
		if f == nil {
			continue
		}
		m[f.ID()] = f
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Router{logger: logger, flows: m}
}

// Has reports whether an intent has a Flow registered. Useful from the
// messages_handler to decide between "flow can handle it" vs "fall back".
func (r *Router) Has(intent string) bool {
	_, ok := r.flows[intent]
	return ok
}

// Handle resolves the right Flow and runs the turn. KindTransfer chains
// are followed transparently. The returned Result is always the *last*
// result in the chain (Trace accumulates all hops).
func (r *Router) Handle(ctx context.Context, in Input) (Result, error) {
	cur := in
	var aggregateTrace []TraceStep

	for hop := 0; hop <= maxTransfers; hop++ {
		f := r.pick(cur)
		if f == nil {
			return Result{
				Kind:  KindError,
				Reply: "Desculpe, não entendi. Pode reformular? (ex.: \"liste meus templates\", \"qual o status do envelope X?\")",
				Trace: aggregateTrace,
			}, ErrUnknownIntent
		}
		res, err := f.Handle(ctx, cur)
		aggregateTrace = append(aggregateTrace, res.Trace...)
		if err != nil {
			res.Trace = aggregateTrace
			return res, err
		}
		if res.Kind != KindTransfer {
			res.Trace = aggregateTrace
			return res, nil
		}
		// Build the input for the transferred-to flow.
		cur = Input{
			Phone:       cur.Phone,
			Session:     cur.Session,
			Intent:      res.NextIntent,
			Entities:    res.NextEntities,
			State:       res.NextState,
			Interact:    nil, // a click was consumed by the previous flow
			Attachments: cur.Attachments,
		}
	}
	return Result{
		Kind:  KindError,
		Reply: "Desculpe, tive um problema interno. Pode tentar de novo daqui a pouco?",
		Trace: aggregateTrace,
	}, errors.New("flow: max transfer hops exceeded")
}

// pick selects the flow according to the dispatch rules.
func (r *Router) pick(in Input) Flow {
	if in.State != nil && in.State.FlowID != "" {
		if f, ok := r.flows[in.State.FlowID]; ok {
			return f
		}
	}
	if in.Intent != "" {
		if f, ok := r.flows[in.Intent]; ok {
			return f
		}
	}
	return nil
}
