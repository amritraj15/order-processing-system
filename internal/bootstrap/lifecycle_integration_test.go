//go:build integration

package bootstrap

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"order_management/api/rest"
	"order_management/api/rest/middleware"
)

// The helper runs the same HTTP lifecycle used by RunServer in a real process.
func TestSignalServerHelper(t *testing.T) {
	if os.Getenv("ORDER_SIGNAL_HELPER") != "1" {
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	e := rest.NewServer(logger)
	limits := middleware.NewRequestLimits(1, time.Second)
	e.Use(limits.Middleware)
	e.GET("/hold", func(c *echo.Context) error {
		logger.Info("request entered")
		select {
		case <-ctx.Done():
			// Stay active across signal propagation, then verify the HTTP context survives.
			time.Sleep(25 * time.Millisecond)
			if err := c.Request().Context().Err(); err != nil {
				return err
			}
			return c.NoContent(204)
		case <-c.Request().Context().Done():
			return c.Request().Context().Err()
		}
	})
	if err := serve(ctx, &http.Server{Addr: "127.0.0.1:0", Handler: e}, limits, func(ctx context.Context) { <-ctx.Done() }, 0, 2*time.Second, logger); err != nil {
		t.Fatal(err)
	}
}
func TestSIGTERMAllowsInFlightResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSignalServerHelper$")
	cmd.Env = append(os.Environ(), "ORDER_SIGNAL_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	events := make(chan map[string]any, 16)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var event map[string]any
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				events <- event
			} else {
				events <- map[string]any{"msg": "helper output", "text": scanner.Text()}
			}
		}
	}()
	waitEvent := func(want string) map[string]any {
		var output []any
		t.Helper()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					t.Fatalf("helper stopped before %s: %v", want, output)
				}
				if event["msg"] == "helper output" {
					output = append(output, event["text"])
				}
				if event["msg"] == want {
					return event
				}
			case <-ctx.Done():
				t.Fatal("helper timeout", ctx.Err())
			}
		}
	}
	started := waitEvent("server started")
	result := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		r, err := client.Get("http://" + started["address"].(string) + "/hold")
		if err == nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			if r.StatusCode != 204 {
				err = fmt.Errorf("in-flight response: HTTP %d", r.StatusCode)
			}
		}
		result <- err
	}()
	waitEvent("request entered")
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal("helper exit", err)
	}
}
