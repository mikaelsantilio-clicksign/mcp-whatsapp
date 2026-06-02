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
// Dispatch rules (precedence high → low):
//
//  1. Interactive reply (a click on a list/button) with an active flow:
//     route to the active flow. The id only makes sense to whoever
//     issued it, so the active flow has the final word here.
//  2. Free text with a recognised NLU intent (anything other than the
//     literal "unknown"): route to the flow registered under that
//     intent — even if there is an open flow. This lets the user
//     change subject mid-conversation ("ah, esquece os templates, lista
//     meus envelopes"), which is how people actually behave on
//     WhatsApp. The NLU's job is to flag "unknown" when it is not
//     confident, so we trust whatever survives that filter.
//  3. Free text without a recognised intent (NLU returned "unknown" or
//     empty) and an active flow exists: route to the active flow so it
//     can interpret short replies like "sim", "ok", a typed-out account
//     id, etc., as continuations of the open conversation.
//  4. Nothing applicable: return KindError with a friendly fallback.
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

// pick selects the flow according to the dispatch rules. See the doc
// comment on Router for the precedence and rationale.
func (r *Router) pick(in Input) Flow {
	hasActiveFlow := in.State != nil && in.State.FlowID != ""

	// 1. A click can only be interpreted by whichever flow asked for it.
	if in.Interact != nil && hasActiveFlow {
		if f, ok := r.flows[in.State.FlowID]; ok {
			return f
		}
	}
	// 2. Free text with a recognised intent overrides any open flow. We
	//    treat the literal "unknown" as "no intent" because that's how
	//    nlu.IntentUnknown serialises.
	if in.Intent != "" && in.Intent != "unknown" {
		if f, ok := r.flows[in.Intent]; ok {
			return f
		}
	}
	// 3. Fall back to the active flow when there is no clear intent.
	//    Short replies like "sim", "ok" or a free-form account id come
	//    through this path.
	if hasActiveFlow {
		if f, ok := r.flows[in.State.FlowID]; ok {
			return f
		}
	}
	return nil
}
