package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/shruggr/go-ordfs-server/loader"
)

type BlockHandler struct {
	loader loader.Loader
}

func NewBlockHandler(ldr loader.Loader) *BlockHandler {
	return &BlockHandler{
		loader: ldr,
	}
}

func (h *BlockHandler) GetLatest(c *fiber.Ctx) error {
	ctx := context.Background()

	height, err := h.loader.LoadChainHeight(ctx)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	headerResp, err := h.loader.LoadHeaderByHeight(ctx, height)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	hash := headerResp.Hash
	if hash == "" {
		hash = hashFromHeader(headerResp.Header)
	}

	response := fiber.Map{
		"height": height,
		"hash":   hash,
	}

	return c.JSON(response)
}

func (h *BlockHandler) GetByHeight(c *fiber.Ctx) error {
	ctx := context.Background()
	heightStr := c.Params("height")

	height, err := strconv.ParseUint(heightStr, 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid height",
		})
	}

	headerResp, err := h.loader.LoadHeaderByHeight(ctx, uint32(height))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	hash := headerResp.Hash
	if hash == "" {
		hash = hashFromHeader(headerResp.Header)
	}

	response := fiber.Map{
		"height": uint32(height),
		"hash":   hash,
	}

	return c.JSON(response)
}

func (h *BlockHandler) GetByHash(c *fiber.Ctx) error {
	ctx := context.Background()
	hash := c.Params("hash")

	if hash == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "hash required",
		})
	}

	headerResp, err := h.loader.LoadHeaderByHash(ctx, hash)
	if err != nil {
		if err == loader.ErrNotFound {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "block not found",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	blockHash := headerResp.Hash
	if blockHash == "" {
		blockHash = hash
	}

	height := heightFromHeader(headerResp.Header)

	response := fiber.Map{
		"height": height,
		"hash":   blockHash,
	}

	return c.JSON(response)
}

func hashFromHeader(header []byte) string {
	return hex.EncodeToString(reverseBytes(doubleSHA256(header)))
}

func heightFromHeader(header []byte) uint32 {
	if len(header) < 80 {
		return 0
	}
	height := uint32(header[68]) | uint32(header[69])<<8 | uint32(header[70])<<16 | uint32(header[71])<<24
	return height
}

func reverseBytes(b []byte) []byte {
	reversed := make([]byte, len(b))
	for i := range b {
		reversed[i] = b[len(b)-1-i]
	}
	return reversed
}

func doubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}
