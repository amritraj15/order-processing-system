package v1

import (
	"github.com/labstack/echo/v5"

	"order_management/api/rest/middleware"
	"order_management/ports"
)

type AuthHandler struct{ Auth ports.Authenticator }
type registerRequest struct {
	Name     string `json:"name" validate:"required,min=2,max=64,nonul"`
	Email    string `json:"email" validate:"required,email,max=255,nonul"`
	Password string `json:"password" validate:"required,min=8,max=72"`
}
type loginRequest struct {
	Email    string `json:"email" validate:"required,email,max=255,nonul"`
	Password string `json:"password" validate:"required,max=72"`
}

func (h *AuthHandler) Register(c *echo.Context) error {
	var req registerRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	session, err := h.Auth.Register(c.Request().Context(), req.Name, req.Email, req.Password)
	if err != nil {
		return err
	}
	return c.JSON(201, session)
}
func (h *AuthHandler) Login(c *echo.Context) error {
	var req loginRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	session, err := h.Auth.Login(c.Request().Context(), req.Email, req.Password)
	if err != nil {
		return err
	}
	return c.JSON(200, session)
}
func (h *AuthHandler) Session(c *echo.Context) error {
	session, err := h.Auth.Session(c.Request().Context(), middleware.Bearer(c))
	if err != nil {
		return err
	}
	return c.JSON(200, session)
}
func (h *AuthHandler) Logout(c *echo.Context) error {
	if err := h.Auth.Logout(c.Request().Context(), middleware.Bearer(c)); err != nil {
		return err
	}
	return c.NoContent(204)
}
