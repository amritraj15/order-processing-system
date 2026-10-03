package v1

import (
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"order_management/api/rest/middleware"
	"order_management/domain/order"
	"order_management/domain/pricing"
	"order_management/internal/logging"
	service "order_management/service/quote"
	"time"
)

type pricingResponse struct {
	Mode             string     `json:"mode"`
	QuoteID          *uuid.UUID `json:"quote_id"`
	Region           *string    `json:"region"`
	MappingVersion   *string    `json:"mapping_version"`
	SourceCurrency   *string    `json:"source_currency"`
	SourceTotalMinor *int64     `json:"source_total_minor"`
	BaseDigits       *int       `json:"base_digits"`
	TargetDigits     *int       `json:"target_digits"`
	Rate             *string    `json:"rate"`
	RateID           *uuid.UUID `json:"rate_id"`
	RateSource       *string    `json:"rate_source"`
	RateValidFrom    *time.Time `json:"rate_valid_from"`
	RateValidUntil   *time.Time `json:"rate_valid_until"`
}

func pricingView(p *pricing.Snapshot, id *uuid.UUID) pricingResponse {
	if p == nil {
		return pricingResponse{Mode: "legacy"}
	}
	v := pricingResponse{Mode: p.Mode, QuoteID: id, SourceCurrency: &p.SourceCurrency, SourceTotalMinor: &p.SourceTotalMinor, BaseDigits: &p.BaseDigits, TargetDigits: &p.TargetDigits, Rate: &p.Rate, RateID: p.RateID, RateSource: &p.RateSource, RateValidFrom: p.RateValidFrom, RateValidUntil: p.RateValidUntil}
	if p.Region != "" {
		v.Region = &p.Region
		v.MappingVersion = &p.MappingVersion
	}
	return v
}

type QuoteHandler struct{ Service *service.Service }
type quoteRequest struct {
	Region string        `json:"region" validate:"omitempty,len=2"`
	Items  []itemRequest `json:"items" validate:"required,min=1,max=100,dive"`
}

func (h *QuoteHandler) Create(c *echo.Context) error {
	var req quoteRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	inputs := make([]order.ItemInput, len(req.Items))
	for i, v := range req.Items {
		inputs[i] = order.ItemInput{ProductID: uuid.MustParse(v.ProductID), Quantity: v.Quantity}
	}
	q, err := h.Service.HandleCreate(c.Request().Context(), service.CreateCommand{CustomerID: uuid.MustParse(middleware.Claims(c).UserID), Region: req.Region, Items: inputs})
	if err != nil {
		return err
	}
	items := orderView(&order.Order{Items: q.Items}).Items
	logging.Mutation(c.Request().Context(), "quote.create", q.ID.String(), "committed", "")
	return c.JSON(201, struct {
		ID               uuid.UUID      `json:"id"`
		Region           string         `json:"region"`
		MappingVersion   string         `json:"mapping_version"`
		BaseCurrency     string         `json:"base_currency"`
		Currency         string         `json:"currency"`
		Rate             string         `json:"rate"`
		RateID           *uuid.UUID     `json:"rate_id"`
		RateSource       string         `json:"rate_source"`
		RateValidFrom    *time.Time     `json:"rate_valid_from"`
		RateValidUntil   *time.Time     `json:"rate_valid_until"`
		ExpiresAt        time.Time      `json:"expires_at"`
		SourceTotalMinor int64          `json:"source_total_minor"`
		TotalMinor       int64          `json:"total_minor"`
		Items            []itemResponse `json:"items"`
	}{q.ID, q.Pricing.Region, q.Pricing.MappingVersion, q.Pricing.SourceCurrency, q.Currency, q.Pricing.Rate, q.Pricing.RateID, q.Pricing.RateSource, q.Pricing.RateValidFrom, q.Pricing.RateValidUntil, q.ExpiresAt, q.Pricing.SourceTotalMinor, q.TotalMinor, items})
}
