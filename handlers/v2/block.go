package v2

import (
	"context"
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

func (h *BlockHandler) GetBlockHeader(c *fiber.Ctx) error {
	ctx := context.Background()
	hashOrHeight := c.Params("hashOrHeight")

	if hashOrHeight == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "hash or height required",
		})
	}

	var headerResp *loader.BlockHeaderResponse
	var err error

	if len(hashOrHeight) == 64 {
		headerResp, err = h.loader.LoadHeaderByHash(ctx, hashOrHeight)
		if err != nil {
			if err == loader.ErrNotFound {
				return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
					"error": "block header not found",
				})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": err.Error(),
			})
		}
		c.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		height, parseErr := strconv.ParseUint(hashOrHeight, 10, 32)
		if parseErr != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "invalid hash or height format",
			})
		}

		headerResp, err = h.loader.LoadHeaderByHeight(ctx, uint32(height))
		if err != nil {
			if err == loader.ErrNotFound {
				return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
					"error": "block header not found",
				})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": err.Error(),
			})
		}

		h.setCacheHeadersByBlockHeight(c, uint32(height))
	}

	c.Set("Content-Type", "application/octet-stream")

	return c.Send(headerResp.Header)
}

func (h *BlockHandler) GetTip(c *fiber.Ctx) error {
	ctx := context.Background()

	tipHeader, err := h.loader.LoadTipHeader(ctx)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	c.Set("Content-Type", "application/octet-stream")
	c.Set("X-Block-Hash", tipHeader.Hash)
	c.Set("X-Block-Height", strconv.FormatUint(uint64(tipHeader.Height), 10))
	c.Set("Cache-Control", "public, max-age=10")

	return c.Send(tipHeader.Header)
}

func (h *BlockHandler) GetChainHeight(c *fiber.Ctx) error {
	ctx := context.Background()

	height, err := h.loader.LoadChainHeight(ctx)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	c.Set("Cache-Control", "public, max-age=10")
	return c.SendString(strconv.FormatUint(uint64(height), 10))
}

func (h *BlockHandler) setCacheHeadersByBlockHeight(c *fiber.Ctx, blockHeight uint32) {
	ctx := context.Background()

	chainHeight, err := h.loader.LoadChainHeight(ctx)
	if err != nil {
		c.Set("Cache-Control", "public, max-age=60")
		return
	}

	depth := chainHeight - blockHeight

	if depth >= 100 {
		c.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if depth >= 4 {
		c.Set("Cache-Control", "public, max-age=3600")
	} else if depth >= 1 {
		c.Set("Cache-Control", "public, max-age=60")
	} else {
		c.Set("Cache-Control", "public, max-age=10")
	}
}
