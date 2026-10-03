package logging

import (
	"context"
	"log/slog"
)

type contextKey int

const (
	requestKey contextKey = iota
	actorKey
)

func WithRequest(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestKey, id)
}
func WithActor(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, actorKey, id)
}

type Handler struct{ slog.Handler }

func (h Handler) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := ctx.Value(requestKey).(string); ok {
		r.AddAttrs(slog.String("request_id", id))
	}
	if id, ok := ctx.Value(actorKey).(string); ok {
		r.AddAttrs(slog.String("actor_id", id))
	}
	return h.Handler.Handle(ctx, r)
}
func (h Handler) WithAttrs(a []slog.Attr) slog.Handler { return Handler{h.Handler.WithAttrs(a)} }
func (h Handler) WithGroup(g string) slog.Handler      { return Handler{h.Handler.WithGroup(g)} }
func Mutation(ctx context.Context, action, resource, outcome, status string) {
	slog.InfoContext(ctx, "mutation", "action", action, "resource_id", resource, "outcome", outcome, "status", status)
}

var _ slog.Handler = Handler{}
