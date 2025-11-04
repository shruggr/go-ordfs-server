package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/swagger"
	"github.com/shruggr/go-ordfs-server/handlers"
)

func setupRoutes(app *fiber.App, contentHandler *handlers.ContentHandler, blockHandler *handlers.BlockHandler, txHandler *handlers.TxHandler, dnsHandler *handlers.DNSHandler, frontendHandler *handlers.FrontendHandler) {
	app.Static("/docs", "./docs")
	app.Static("/public", "./frontend/public")

	app.Get("/docs/*", swagger.New(swagger.Config{
		URL:          "/docs/swagger.yaml",
		DeepLinking:  true,
		DocExpansion: "list",
		TryItOutEnabled: true,
	}))

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status": "ok",
		})
	})

	// Block endpoints (backward compatible)
	app.Get("/block/latest", blockHandler.GetLatest)
	app.Get("/block/height/:height", blockHandler.GetByHeight)
	app.Get("/block/hash/:hash", blockHandler.GetByHash)

	// Block endpoints (v1/bsv prefix for production compatibility)
	app.Get("/v1/bsv/block/latest", blockHandler.GetLatest)
	app.Get("/v1/bsv/block/height/:height", blockHandler.GetByHeight)
	app.Get("/v1/bsv/block/hash/:hash", blockHandler.GetByHash)
	app.Get("/tx/:txid", txHandler.GetRawTx)

	app.Get("/preview/:b64HtmlData", frontendHandler.RenderPreview)
	app.Post("/preview", frontendHandler.RenderPreviewPost)

	app.Get("/content/*", contentHandler.HandleAll)
	app.Get("/*", dnsHandler.HandleAll)
}
