package rest

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"math/rand/v2"
	"mime"
	"net/http"
	"order_management/internal/logging"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"

	"order_management/api/validator"
	"order_management/domain/shared"
	"order_management/ports"
)

type APIError struct {
	Error   string            `json:"error"`
	Details map[string]string `json:"details,omitempty"`
}
type jsonBinder struct{}

func (jsonBinder) Bind(c *echo.Context, value any) error {
	media, _, err := mime.ParseMediaType(c.Request().Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return echo.NewHTTPError(http.StatusUnsupportedMediaType, "Content-Type must be application/json")
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 1<<20)
	decoder := json.NewDecoder(c.Request().Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return echo.NewHTTPError(400, "malformed JSON request")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return echo.NewHTTPError(400, "expected a single JSON value")
	}
	return validator.Struct(value)
}
func NewServer(logger *slog.Logger) *echo.Echo {
	e := echo.New()
	e.Binder = jsonBinder{}
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			id := uuid.NewString()
			c.Response().Header().Set("X-Request-ID", id)
			c.SetRequest(c.Request().WithContext(logging.WithRequest(c.Request().Context(), id)))
			start := time.Now()
			err := next(c)
			if err != nil {
				e.HTTPErrorHandler(c, err)
			}
			status := 200
			if response, unwrapErr := echo.UnwrapResponse(c.Response()); unwrapErr == nil {
				status = response.Status
			}
			logger.InfoContext(c.Request().Context(), "request completed", "method", c.Request().Method, "route", c.Path(), "status", status, "duration", time.Since(start))
			return nil
		}
	})
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) (err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.ErrorContext(c.Request().Context(), "request panic", "operation", c.Path(), "stack", logging.StackFrames())
					err = echo.NewHTTPError(500, "internal server error")
				}
			}()
			return next(c)
		}
	})
	e.HTTPErrorHandler = func(c *echo.Context, err error) {
		if res, unwrapErr := echo.UnwrapResponse(c.Response()); unwrapErr == nil && res.Committed {
			return
		}
		code := 500
		response := APIError{Error: "internal server error"}
		var validation *validator.ValidationError
		var httpErr *echo.HTTPError
		var statusCoder echo.HTTPStatusCoder
		retryableFailure := errors.Is(err, shared.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded)
		switch {
		case errors.Is(err, context.Canceled):
			// 499 distinguishes caller cancellation; disconnected callers receive nothing.
			logger.DebugContext(c.Request().Context(), "request canceled", "operation", c.Path())
			_ = c.JSON(499, APIError{Error: "request canceled"})
			return
		case retryableFailure:
			code = 503
			c.Response().Header().Set("Retry-After", strconv.Itoa(1+rand.IntN(3)))
		case errors.Is(err, shared.ErrIdempotencyConflict):
			code = 409
			response = APIError{Error: "idempotency key reused with different payload", Details: map[string]string{"reason": "idempotency_key_conflict"}}
		case errors.As(err, &validation):
			code = 422
			response = APIError{Error: "validation failed", Details: validation.Details}
		case errors.Is(err, shared.ErrQuoteExpired), errors.Is(err, shared.ErrRateUnavailable):
			code = 409
			reason := "quote_expired"
			if errors.Is(err, shared.ErrRateUnavailable) {
				reason = "rate_unavailable"
			}
			response = APIError{Error: "pricing unavailable", Details: map[string]string{"reason": reason}}
		case errors.Is(err, shared.ErrAmountRange):
			code = 422
			response = APIError{Error: "amount out of range", Details: map[string]string{"reason": "amount_out_of_range"}}
		case errors.Is(err, shared.ErrInvalid):
			code = 422
			response.Error = "invalid input"
		case errors.Is(err, ports.ErrInvalidCredentials), errors.Is(err, ports.ErrInvalidToken):
			code = 401
			response.Error = "invalid credentials or token"
		case errors.Is(err, shared.ErrNotFound):
			code = 404
			response.Error = "not found"
		case errors.Is(err, shared.ErrConflict):
			code = 409
			response.Error = "duplicate resource or invalid order state"
		case errors.Is(err, shared.ErrForbidden):
			code = 403
			response.Error = "forbidden"
		case errors.As(err, &httpErr):
			code = httpErr.Code
			response.Error = httpErr.Message
		case errors.As(err, &statusCoder):
			code = statusCoder.StatusCode()
			response.Error = http.StatusText(code)
		}
		if code >= 500 {
			attrs := append(logging.ErrorAttrs(err), "operation", c.Path())
			if retryableFailure {
				logger.WarnContext(c.Request().Context(), "request unavailable", attrs...)
			} else {
				logger.ErrorContext(c.Request().Context(), "request failed", attrs...)
			}
			response.Error = "internal server error"
			if retryableFailure {
				response.Error = "temporarily unavailable"
			}
		}
		_ = c.JSON(code, response)
	}
	return e
}
