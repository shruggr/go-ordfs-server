package api

import (
	"log"

	"github.com/b-open-io/overlay/headers"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/shruggr/go-ordfs-server/cache"
	"github.com/shruggr/go-ordfs-server/config"
	"github.com/shruggr/go-ordfs-server/handlers"
	"github.com/shruggr/go-ordfs-server/loader"
)

func NewServer(cfg *config.Config, redisCache *cache.RedisCache) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName: "go-ordfs-server",
	})

	// app.Use(recover.New())
	app.Use(logger.New())
	app.Use(cors.New())

	headersClient := headers.NewClient(headers.ClientParams{
		Url:    cfg.BlockHeadersURL,
		ApiKey: cfg.BlockHeadersToken,
	})

	frontendHandler, err := handlers.NewFrontendHandler()
	if err != nil {
		log.Fatalf("Failed to initialize frontend handler: %v", err)
	}

	ldr := loader.NewJungleBusLoader(redisCache.Client(), cfg.JunglebusURL)
	contentHandler := handlers.NewContentHandler(ldr, redisCache)
	blockHandler := handlers.NewBlockHandler(headersClient, ldr)
	txHandler := handlers.NewTxHandler(ldr, headersClient)
	dnsHandler := handlers.NewDNSHandler(ldr, frontendHandler, redisCache, cfg.OrdfsHost)

	setupRoutes(app, contentHandler, blockHandler, txHandler, dnsHandler, frontendHandler)

	return app
}
