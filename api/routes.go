package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/swagger"
	"github.com/shruggr/go-ordfs-server/handlers"
	v1 "github.com/shruggr/go-ordfs-server/handlers/v1"
	v2 "github.com/shruggr/go-ordfs-server/handlers/v2"
)

func setupRoutes(app *fiber.App, contentHandler *handlers.ContentHandler, v1BlockHandler *v1.BlockHandler, v1TxHandler *v1.TxHandler, v2BlockHandler *v2.BlockHandler, v2TxHandler *v2.TxHandler, dnsHandler *handlers.DNSHandler, frontendHandler *handlers.FrontendHandler) {
	app.Static("/public", "./frontend/public")

	// Serve v1 swagger file
	app.Get("/v1/docs/swagger.yaml", func(c *fiber.Ctx) error {
		return c.SendFile("./docs/v1.swagger.yaml")
	})
	app.Get("/v1/docs/*", swagger.New(swagger.Config{
		URL:             "/v1/docs/swagger.yaml",
		DeepLinking:     true,
		DocExpansion:    "list",
		TryItOutEnabled: true,
	}))

	// Serve v2 swagger file
	app.Get("/v2/docs/swagger.yaml", func(c *fiber.Ctx) error {
		return c.SendFile("./docs/v2.swagger.yaml")
	})
	app.Get("/v2/docs/*", swagger.New(swagger.Config{
		URL:             "/v2/docs/swagger.yaml",
		DeepLinking:     true,
		DocExpansion:    "list",
		TryItOutEnabled: true,
	}))

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status": "ok",
		})
	})

	// V1 API endpoints
	app.Get("/v1/bsv/block/latest", v1BlockHandler.GetLatest)
	app.Get("/v1/bsv/block/height/:height", v1BlockHandler.GetByHeight)
	app.Get("/v1/bsv/block/hash/:hash", v1BlockHandler.GetByHash)
	app.Get("/v1/bsv/tx/:txid", v1TxHandler.GetRawTx)

	// V2 API endpoints
	app.Get("/v2/tx/:txid", v2TxHandler.GetRawTx)
	app.Get("/v2/tx/:txid/proof", v2TxHandler.GetMerkleProof)
	app.Get("/v2/tx/:txid/beef", v2TxHandler.GetBeef)
	app.Get("/v2/tx/:txid/:outputIndex", v2TxHandler.GetOutput)
	app.Get("/v2/block/tip", v2BlockHandler.GetTip)
	app.Head("/v2/block/tip", v2BlockHandler.GetTip)
	app.Get("/v2/chain/height", v2BlockHandler.GetChainHeight)
	app.Get("/v2/block/:hashOrHeight", v2BlockHandler.GetBlockHeader)

	app.Get("/preview/:b64HtmlData", frontendHandler.RenderPreview)
	app.Post("/preview", frontendHandler.RenderPreviewPost)

	app.Get("/content/*", contentHandler.HandleAll)
	app.Get("/*", dnsHandler.HandleAll)
}
