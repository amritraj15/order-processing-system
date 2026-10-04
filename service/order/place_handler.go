package order

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	domain "order_management/domain/order"
	"order_management/domain/shared"
	"order_management/service/uow"
)

type PlaceCommand struct {
	CustomerID     uuid.UUID
	Items          []domain.ItemInput
	QuoteID        *uuid.UUID
	IdempotencyKey string
}

// ValidIdempotencyKey accepts opaque, case-sensitive keys without whitespace.
func ValidIdempotencyKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

func requestHash(cmd PlaceCommand) (string, error) {
	if cmd.CustomerID == uuid.Nil || !ValidIdempotencyKey(cmd.IdempotencyKey) {
		return "", shared.ErrInvalid
	}
	if cmd.QuoteID != nil {
		if *cmd.QuoteID == uuid.Nil || cmd.Items != nil {
			return "", shared.ErrInvalid
		}
	} else {
		if len(cmd.Items) < 1 || len(cmd.Items) > 100 {
			return "", shared.ErrInvalid
		}
		seen := make(map[uuid.UUID]bool, len(cmd.Items))
		for _, item := range cmd.Items {
			if item.ProductID == uuid.Nil || item.Quantity <= 0 || seen[item.ProductID] {
				return "", shared.ErrInvalid
			}
			seen[item.ProductID] = true
		}
	}
	// Hash decoded input, not raw JSON: whitespace/object-key order and UUID
	// spelling are irrelevant. Item array order remains part of the request.
	// Keep this encoding stable so stored keys survive application upgrades.
	payload, err := json.Marshal(struct {
		Version int                `json:"version"`
		QuoteID *uuid.UUID         `json:"quote_id,omitempty"`
		Items   []domain.ItemInput `json:"items,omitempty"`
	}{Version: 1, QuoteID: cmd.QuoteID, Items: cmd.Items})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

// HandlePlace requires a key and commits the order, its items and its durable key
// together. A replay returns the original order with its current status.
func (s *Service) HandlePlace(ctx context.Context, cmd PlaceCommand) (*domain.Order, bool, error) {
	hash, err := requestHash(cmd)
	if err != nil {
		return nil, false, fmt.Errorf("create order: %w", err)
	}
	// Includes waiting for a pooled connection, all statements and commit.
	// WithTimeout preserves an earlier caller deadline.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var result *domain.Order
	replay := false
	err = s.UOW.Do(ctx, func(repos uow.Repositories) error {
		record, err := repos.Orders().LockAndFindIdempotency(ctx, cmd.CustomerID, cmd.IdempotencyKey)
		if err == nil {
			if record.RequestHash != hash {
				return shared.ErrIdempotencyConflict
			}
			result, err = repos.Orders().Get(ctx, record.OrderID, &cmd.CustomerID)
			replay = true
			return err
		}
		if !errors.Is(err, shared.ErrNotFound) {
			return err
		}
		if cmd.QuoteID != nil {
			result, replay, err = createFromQuote(ctx, repos, cmd.CustomerID, *cmd.QuoteID)
		} else {
			result, err = createItems(ctx, repos, cmd)
		}
		if err != nil {
			return err
		}
		return repos.Orders().InsertIdempotency(ctx, domain.IdempotencyRecord{CustomerID: cmd.CustomerID, Key: cmd.IdempotencyKey, RequestHash: hash, OrderID: result.ID})
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errors.Join(err, ctx.Err())
		}
		return nil, false, fmt.Errorf("create order: %w", err)
	}
	return result, replay, nil
}
