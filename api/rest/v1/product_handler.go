package v1

import (
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"order_management/domain/product"
	"order_management/internal/logging"
	service "order_management/service/product"
)

type ProductHandler struct {
	Service  *service.Service
	Currency string
}
type createProductRequest struct {
	SKU        string `json:"sku" validate:"required,max=64,nonul"`
	Name       string `json:"name" validate:"required,max=255,nonul"`
	PriceMinor int64  `json:"price_minor" validate:"gt=0"`
}
type productResponse struct {
	ID         uuid.UUID `json:"id"`
	SKU        string    `json:"sku"`
	Name       string    `json:"name"`
	PriceMinor int64     `json:"price_minor"`
	Currency   string    `json:"currency"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (h *ProductHandler) view(p *product.Product) productResponse {
	return productResponse{ID: p.ID, SKU: p.SKU, Name: p.Name, PriceMinor: p.PriceMinor, Currency: h.Currency, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}
func (h *ProductHandler) Create(c *echo.Context) error {
	var req createProductRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	p, err := h.Service.HandleCreate(c.Request().Context(), service.CreateCommand{SKU: req.SKU, Name: req.Name, PriceMinor: req.PriceMinor})
	if err != nil {
		return err
	}
	logging.Mutation(c.Request().Context(), "product.create", p.ID.String(), "committed", "")
	c.Response().Header().Set("Location", "/api/v1/products/"+p.ID.String())
	return c.JSON(201, h.view(p))
}
func (h *ProductHandler) Get(c *echo.Context) error {
	id, err := parseID(c)
	if err != nil {
		return err
	}
	p, err := h.Service.HandleGet(c.Request().Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(200, h.view(p))
}
func (h *ProductHandler) List(c *echo.Context) error {
	pagination, err := parsePagination(c)
	if err != nil {
		return err
	}
	page, err := h.Service.HandleList(c.Request().Context(), pagination)
	if err != nil {
		return err
	}
	response := pageResponse[productResponse]{Items: make([]productResponse, len(page.Items)), NextCursor: page.NextCursor}
	for i := range page.Items {
		response.Items[i] = h.view(&page.Items[i])
	}
	return c.JSON(200, response)
}
