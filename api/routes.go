package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/swagger"
	"github.com/shruggr/go-ordfs-server/handlers"
)

func setupRoutes(app *fiber.App, contentHandler *handlers.ContentHandler, blockHandler *handlers.BlockHandler, txHandler *handlers.TxHandler, dnsHandler *handlers.DNSHandler, frontendHandler *handlers.FrontendHandler) {
	app.Static("/v1/docs", "./docs")
	app.Static("/public", "./frontend/public")

	app.Get("/v1/docs/*", swagger.New(swagger.Config{
		URL:          "/v1/docs/swagger.yaml",
		DeepLinking:  true,
		DocExpansion: "list",
		TryItOutEnabled: true,
	}))

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status": "ok",
		})
	})

	// Block and transaction endpoints
	app.Get("/v1/bsv/block/latest", blockHandler.GetLatest)
	app.Get("/v1/bsv/block/height/:height", blockHandler.GetByHeight)
	app.Get("/v1/bsv/block/hash/:hash", blockHandler.GetByHash)
	app.Get("/v1/bsv/tx/:txid", txHandler.GetRawTx)

	app.Get("/preview/:b64HtmlData", frontendHandler.RenderPreview)
	app.Post("/preview", frontendHandler.RenderPreviewPost)

	app.Get("/content/*", contentHandler.HandleAll)
	app.Get("/*", dnsHandler.HandleAll)
}
