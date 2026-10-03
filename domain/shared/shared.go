package shared

import (
	"errors"

	"github.com/google/uuid"
)

var (
	ErrUnavailable         = errors.New("temporarily unavailable")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different payload")
	ErrQuoteExpired        = errors.New("quote expired")
	ErrRateUnavailable     = errors.New("rate unavailable")
	ErrAmountRange         = errors.New("amount out of range")
	ErrNotFound            = errors.New("not found")
	ErrConflict            = errors.New("conflict")
	ErrInvalid             = errors.New("invalid input")
	ErrForbidden           = errors.New("forbidden")
)

type Pagination struct {
	Limit  int
	Cursor *uuid.UUID
}
type Page[T any] struct {
	Items      []T
	NextCursor *uuid.UUID
}
