package docs

import (
	_ "embed"

	"github.com/labstack/echo/v5"
)

//go:embed openapi.yaml
var spec []byte

const swaggerHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Order API</title><link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css"></head><body><div id="swagger-ui"></div><script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script><script>SwaggerUIBundle({url:'/api/swagger/openapi.yaml',dom_id:'#swagger-ui',persistAuthorization:false});</script></body></html>`

func Mount(e *echo.Echo) {
	e.GET("/api/swagger/openapi.yaml", func(c *echo.Context) error { return c.Blob(200, "application/yaml", spec) })
	e.GET("/api/swagger/index.html", func(c *echo.Context) error { return c.HTML(200, swaggerHTML) })
}
