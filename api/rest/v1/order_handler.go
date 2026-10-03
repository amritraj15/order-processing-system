package v1

import (
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"order_management/api/rest/middleware"
	"order_management/domain/order"
	"order_management/domain/shared"
	"order_management/domain/user"
	"order_management/internal/logging"
	service "order_management/service/order"
)

type OrderHandler struct{ Service *service.Service }
type itemRequest struct {
	ProductID string `json:"product_id" validate:"required,uuid"`
	Quantity  int64  `json:"quantity" validate:"gt=0"`
}
type createOrderRequest struct {
	QuoteID string        `json:"quote_id" validate:"omitempty,uuid"`
	Items   []itemRequest `json:"items" validate:"omitempty,min=1,max=100,dive"`
}
type statusRequest struct {
	Status order.Status `json:"status" validate:"required,oneof=PROCESSING SHIPPED DELIVERED"`
}
type itemResponse struct {
	SourceUnitPriceMinor *int64    `json:"source_unit_price_minor"`
	SourceLineTotalMinor *int64    `json:"source_line_total_minor"`
	ProductID            uuid.UUID `json:"product_id"`
	SKU                  string    `json:"sku"`
	Name                 string    `json:"name"`
	Quantity             int64     `json:"quantity"`
	UnitPriceMinor       int64     `json:"unit_price_minor"`
	LineTotalMinor       int64     `json:"line_total_minor"`
}
type orderResponse struct {
	Pricing    pricingResponse `json:"pricing"`
	ID         uuid.UUID       `json:"id"`
	CustomerID uuid.UUID       `json:"customer_id"`
	Status     order.Status    `json:"status"`
	Currency   string          `json:"currency"`
	TotalMinor int64           `json:"total_minor"`
	Items      []itemResponse  `json:"items"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

func orderView(o *order.Order) orderResponse {
	view := orderResponse{Pricing: pricingView(o.Pricing, o.QuoteID), ID: o.ID, CustomerID: o.CustomerID, Status: o.Status, Currency: o.Currency, TotalMinor: o.TotalMinor, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt, Items: make([]itemResponse, len(o.Items))}
	for i, item := range o.Items {
		view.Items[i] = itemResponse{SourceUnitPriceMinor: item.SourceUnitPriceMinor, SourceLineTotalMinor: item.SourceLineTotalMinor, ProductID: item.ProductID, SKU: item.SKU, Name: item.Name, Quantity: item.Quantity, UnitPriceMinor: item.UnitPriceMinor, LineTotalMinor: item.LineTotalMinor}
	}
	return view
}

type pageResponse[T any] struct {
	Items      []T        `json:"items"`
	NextCursor *uuid.UUID `json:"next_cursor"`
}

func parseID(c *echo.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, echo.NewHTTPError(422, "invalid resource ID")
	}
	return id, nil
}
func parsePagination(c *echo.Context) (shared.Pagination, error) {
	p := shared.Pagination{Limit: 20}
	if raw := c.QueryParam("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			return p, echo.NewHTTPError(422, "limit must be 1–100")
		}
		p.Limit = limit
	}
	if raw := c.QueryParam("cursor"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			return p, echo.NewHTTPError(422, "invalid cursor")
		}
		p.Cursor = &id
	}
	return p, nil
}
func customerScope(c *echo.Context) *uuid.UUID {
	claims := middleware.Claims(c)
	if claims.Role == string(user.Admin) {
		return nil
	}
	id := uuid.MustParse(claims.UserID)
	return &id
}
func (h *OrderHandler) Create(c *echo.Context) error {
	var req createOrderRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	var o *order.Order
	var err error
	replay := false
	customer := uuid.MustParse(middleware.Claims(c).UserID)
	if req.QuoteID != "" {
		if req.Items != nil {
			return c.JSON(422, map[string]any{"error": "quote_id and items are mutually exclusive", "details": map[string]string{"reason": "quote_required_fields"}})
		}
		id := uuid.MustParse(req.QuoteID)
		if id == uuid.Nil {
			return shared.ErrInvalid
		}
		o, replay, err = h.Service.HandleCreateFromQuote(c.Request().Context(), service.CreateFromQuoteCommand{CustomerID: customer, QuoteID: id})
	} else {
		if len(req.Items) == 0 {
			return shared.ErrInvalid
		}
		inputs := make([]order.ItemInput, len(req.Items))
		for i, item := range req.Items {
			inputs[i] = order.ItemInput{ProductID: uuid.MustParse(item.ProductID), Quantity: item.Quantity}
		}
		o, err = h.Service.HandleCreate(c.Request().Context(), service.CreateCommand{CustomerID: customer, Items: inputs})
	}
	if err != nil {
		return err
	}
	code, outcome := 201, "committed"
	if replay {
		code = 200
		outcome = "replayed"
	}
	logging.Mutation(c.Request().Context(), "order.create", o.ID.String(), outcome, string(o.Status))
	c.Response().Header().Set("Location", "/api/v1/orders/"+o.ID.String())
	return c.JSON(code, orderView(o))
}
func (h *OrderHandler) Get(c *echo.Context) error {
	id, err := parseID(c)
	if err != nil {
		return err
	}
	o, err := h.Service.HandleGet(c.Request().Context(), id, customerScope(c))
	if err != nil {
		return err
	}
	return c.JSON(200, orderView(o))
}
func (h *OrderHandler) List(c *echo.Context) error {
	pagination, err := parsePagination(c)
	if err != nil {
		return err
	}
	status := order.Status(c.QueryParam("status"))
	if status != "" && !status.Valid() {
		return echo.NewHTTPError(422, "invalid order status")
	}
	page, err := h.Service.HandleList(c.Request().Context(), order.Filter{CustomerID: customerScope(c), Status: status, Pagination: pagination})
	if err != nil {
		return err
	}
	response := pageResponse[orderResponse]{Items: make([]orderResponse, len(page.Items)), NextCursor: page.NextCursor}
	for i := range page.Items {
		response.Items[i] = orderView(&page.Items[i])
	}
	return c.JSON(200, response)
}
func (h *OrderHandler) Status(c *echo.Context) error {
	id, err := parseID(c)
	if err != nil {
		return err
	}
	var req statusRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	o, err := h.Service.HandleStatus(c.Request().Context(), service.StatusCommand{ID: id, Status: req.Status})
	if err != nil {
		return err
	}
	logging.Mutation(c.Request().Context(), "order.status", o.ID.String(), "committed", string(o.Status))
	return c.JSON(200, orderView(o))
}
func (h *OrderHandler) Cancel(c *echo.Context) error {
	id, err := parseID(c)
	if err != nil {
		return err
	}
	o, err := h.Service.HandleCancel(c.Request().Context(), service.CancelCommand{ID: id, CustomerID: uuid.MustParse(middleware.Claims(c).UserID)})
	if err != nil {
		return err
	}
	logging.Mutation(c.Request().Context(), "order.cancel", o.ID.String(), "committed", string(o.Status))
	return c.JSON(200, orderView(o))
}
