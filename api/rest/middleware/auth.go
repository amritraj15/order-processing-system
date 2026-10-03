package middleware

import (
	"strings"

	"github.com/labstack/echo/v5"

	"order_management/domain/shared"
	"order_management/domain/user"
	"order_management/internal/logging"
	"order_management/ports"
)

const AuthClaimsKey = "auth_claims"

func Bearer(c *echo.Context) string {
	parts := strings.Fields(c.Request().Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}
func Claims(c *echo.Context) *ports.AuthClaims {
	claims, _ := c.Get(AuthClaimsKey).(*ports.AuthClaims)
	return claims
}
func RequireAuth(auth ports.Authenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			raw := Bearer(c)
			if raw == "" {
				return echo.NewHTTPError(401, "authentication required")
			}
			claims, err := auth.ValidateToken(c.Request().Context(), raw)
			if err != nil {
				return err
			}
			if claims == nil || claims.UserID == "" {
				return ports.ErrInvalidToken
			}
			c.Set(AuthClaimsKey, claims)
			c.SetRequest(c.Request().WithContext(logging.WithActor(c.Request().Context(), claims.UserID)))
			return next(c)
		}
	}
}
func RequireRole(role user.Role) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			claims := Claims(c)
			if claims == nil {
				return ports.ErrInvalidToken
			}
			if claims.Role != string(role) {
				return shared.ErrForbidden
			}
			return next(c)
		}
	}
}
