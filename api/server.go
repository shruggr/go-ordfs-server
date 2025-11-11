package api

import (
	"log"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/shruggr/go-ordfs-server/cache"
	"github.com/shruggr/go-ordfs-server/config"
	"github.com/shruggr/go-ordfs-server/handlers"
	v1 "github.com/shruggr/go-ordfs-server/handlers/v1"
	v2 "github.com/shruggr/go-ordfs-server/handlers/v2"
	"github.com/shruggr/go-ordfs-server/loader"
)

func NewServer(cfg *config.Config, redisCache *cache.RedisCache) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName: "go-ordfs-server",
	})

	// app.Use(recover.New())
	app.Use(logger.New())
	app.Use(cors.New())

	frontendHandler, err := handlers.NewFrontendHandler()
	if err != nil {
		log.Fatalf("Failed to initialize frontend handler: %v", err)
	}

	ldr := loader.NewJungleBusLoader(redisCache.Client(), cfg.JunglebusURL)
	contentHandler := handlers.NewContentHandler(ldr, redisCache)
	v1BlockHandler := v1.NewBlockHandler(ldr)
	v1TxHandler := v1.NewTxHandler(ldr)
	v2BlockHandler := v2.NewBlockHandler(ldr)
	v2TxHandler := v2.NewTxHandler(ldr)
	v2MetadataHandler := v2.NewMetadataHandler(ldr, redisCache)
	dnsHandler := handlers.NewDNSHandler(ldr, frontendHandler, redisCache, cfg.OrdfsHost)

	setupRoutes(app, contentHandler, v1BlockHandler, v1TxHandler, v2BlockHandler, v2TxHandler, v2MetadataHandler, dnsHandler, frontendHandler)

	return app
}
