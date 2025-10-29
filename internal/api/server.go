package api

import (
	"log"

	"github.com/b-open-io/overlay/headers"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/shruggr/go-ordfs-server/internal/cache"
	"github.com/shruggr/go-ordfs-server/internal/config"
	"github.com/shruggr/go-ordfs-server/internal/handlers"
	"github.com/shruggr/go-ordfs-server/internal/txloader"
)

func NewServer(cfg *config.Config, redisCache *cache.RedisCache) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName: "go-ordfs-server",
	})

	app.Use(recover.New())
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

	txLoader := txloader.New(redisCache.Client(), cfg.JunglebusURL)
	contentHandler := handlers.NewContentHandler(txLoader, redisCache)
	blockHandler := handlers.NewBlockHandler(headersClient)
	txHandler := handlers.NewTxHandler(txLoader)
	directoryHandler := handlers.NewDirectoryHandler(contentHandler)
	dnsHandler := handlers.NewDNSHandler(contentHandler, directoryHandler, frontendHandler, redisCache, cfg.OrdfsHost)

	setupRoutes(app, contentHandler, blockHandler, txHandler, directoryHandler, dnsHandler, frontendHandler)

	return app
}
