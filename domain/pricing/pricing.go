package pricing

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type Settings struct {
	BaseCurrency  string
	InitializedAt time.Time
}
type InitializeInput struct {
	Currency        string
	ConfirmExisting bool
}
type Rate struct {
	ID                                         uuid.UUID
	BaseCurrency, TargetCurrency, Rate, Source string
	ValidFrom, ValidUntil, CreatedAt           time.Time
}
type Snapshot struct {
	Mode, Region, MappingVersion, SourceCurrency, Rate, RateSource string
	BaseDigits, TargetDigits                                       int
	RateID                                                         *uuid.UUID
	RateValidFrom, RateValidUntil                                  *time.Time
	SourceTotalMinor                                               int64
}
type Repository interface {
	Settings(context.Context) (*Settings, error)
	Initialize(context.Context, InitializeInput) (*Settings, error)
	ActiveRate(context.Context, string, string, time.Time) (*Rate, error)
	ImportRate(context.Context, *Rate) error
}
