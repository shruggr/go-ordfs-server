package handlers

import (
	"context"
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/shruggr/go-ordfs-server/cache"
	"github.com/shruggr/go-ordfs-server/loader"
	"github.com/shruggr/go-ordfs-server/ordfs"
)

type ContentHandler struct {
	ordfs    *ordfs.Ordfs
	resolver *DirectoryResolver
}

func NewContentHandler(ldr loader.Loader, redisCache *cache.RedisCache) *ContentHandler {
	ordfsInstance := ordfs.New(ldr, redisCache.Client())
	return &ContentHandler{
		ordfs:    ordfsInstance,
		resolver: NewDirectoryResolver(ordfsInstance),
	}
}

func (h *ContentHandler) HandleAll(c *fiber.Ctx) error {
	ctx := context.Background()

	parsed, err := ParsePointerPath(c.Path(), "/content")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("invalid path: %v", err),
		})
	}

	return h.resolver.Resolve(ctx, c, parsed.Pointer, parsed.Seq, parsed.FilePath)
}
