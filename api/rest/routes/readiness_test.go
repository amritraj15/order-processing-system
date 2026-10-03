package routes_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"order_management/api/rest"
	"order_management/api/rest/routes"
	"order_management/service/processing"
	"strings"
	"testing"
	"time"
)

func TestReadinessReportsWorkerFailureWithHealthyDatabase(t *testing.T) {
	status := processing.NewStatus(time.Now(), time.Minute)
	e := rest.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	routes.Mount(e, routes.MountConfig{Readiness: func(context.Context) error { return nil }, WorkerStatus: status})
	check := func(want int, contains string) {
		t.Helper()
		r := httptest.NewRecorder()
		e.ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/ready", nil))
		if r.Code != want || !strings.Contains(r.Body.String(), contains) {
			t.Fatalf("readiness %d %s", r.Code, r.Body.String())
		}
	}
	check(200, "starting")
	status.Start(time.Now())
	status.Finish(time.Now(), errors.New("private database details"))
	check(503, "failed")
	status.Start(time.Now())
	status.Finish(time.Now(), nil)
	check(200, `"worker":"ok"`)
}
