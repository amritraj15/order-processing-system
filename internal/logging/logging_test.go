package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestContextSurvivesLoggerAttributes(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(Handler{slog.NewJSONHandler(&buf, nil)}).With("component", "test")
	ctx := WithActor(WithRequest(context.Background(), "request"), "actor")
	logger.InfoContext(ctx, "mutation", "action", "order.create", "outcome", "committed")
	var fields map[string]any
	if err := json.Unmarshal(buf.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["request_id"] != "request" || fields["actor_id"] != "actor" || fields["action"] != "order.create" {
		t.Fatalf("missing context: %v", fields)
	}
}
