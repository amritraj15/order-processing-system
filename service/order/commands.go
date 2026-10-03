package order

import (
	"github.com/google/uuid"

	domain "order_management/domain/order"
)

type CreateCommand struct {
	CustomerID uuid.UUID
	Items      []domain.ItemInput
}
type StatusCommand struct {
	ID     uuid.UUID
	Status domain.Status
}
type CancelCommand struct{ ID, CustomerID uuid.UUID }
