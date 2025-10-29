package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/shruggr/go-ordfs-server/internal/api"
	"github.com/shruggr/go-ordfs-server/internal/cache"
	"github.com/shruggr/go-ordfs-server/internal/config"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found")
	}

	cfg := config.Load()

	redisCache, err := cache.NewRedisCache(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize Redis: %v", err)
	}
	defer redisCache.Close()

	server := api.NewServer(cfg, redisCache)

	go func() {
		if err := server.Listen(cfg.Port); err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")
	if err := server.Shutdown(); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
