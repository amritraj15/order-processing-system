package ports

import (
	"context"
	"time"
)

type PendingProcessor interface {
	ProcessBatch(context.Context, time.Time, int) (int, error)
}
