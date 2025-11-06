package handlers

import (
	"context"
	"strconv"

	"github.com/b-open-io/overlay/headers"
	"github.com/gofiber/fiber/v2"
	"github.com/shruggr/go-ordfs-server/loader"
)

type BlockHandler struct {
	headersClient *headers.Client
	loader        loader.Loader
}

func NewBlockHandler(headersClient *headers.Client, ldr loader.Loader) *BlockHandler {
	return &BlockHandler{
		headersClient: headersClient,
		loader:        ldr,
	}
}

func (h *BlockHandler) GetLatest(c *fiber.Ctx) error {
	ctx := context.Background()

	chaintip, err := h.headersClient.GetChaintip(ctx)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	// Flatten the structure - combine root level fields with header fields
	response := fiber.Map{
		"height":            chaintip.Height,
		"hash":              chaintip.Header.Hash.String(),
		"version":           chaintip.Header.Version,
		"prevBlockHash":     chaintip.Header.PreviousBlock.String(),
		"merkleRoot":        chaintip.Header.MerkleRoot.String(),
		"creationTimestamp": chaintip.Header.Timestamp,
		"difficultyTarget":  chaintip.Header.Bits,
		"nonce":             chaintip.Header.Nonce,
		"state":             chaintip.State,
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

	header, err := h.headersClient.BlockByHeight(ctx, uint32(height))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	return c.JSON(header)
}

func (h *BlockHandler) GetByHash(c *fiber.Ctx) error {
	ctx := context.Background()
	hash := c.Params("hash")

	if hash == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "hash required",
		})
	}

	state, err := h.headersClient.GetBlockState(ctx, hash)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	return c.JSON(state)
}

func (h *BlockHandler) GetBlockHeader(c *fiber.Ctx) error {
	ctx := context.Background()
	hashOrHeight := c.Params("hashOrHeight")

	if hashOrHeight == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "hash or height required",
		})
	}

	var header []byte
	var err error
	var blockHeight uint32

	if len(hashOrHeight) == 64 {
		header, err = h.loader.LoadHeaderByHash(ctx, hashOrHeight)
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

		header, blockHeight, err = h.loader.LoadHeaderByHeight(ctx, uint32(height))
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

		h.setCacheHeadersByBlockHeight(c, blockHeight)
	}

	c.Set("Content-Type", "application/octet-stream")
	return c.Send(header)
}

func (h *BlockHandler) setCacheHeadersByBlockHeight(c *fiber.Ctx, blockHeight uint32) {
	ctx := context.Background()

	chaintip, err := h.headersClient.GetChaintip(ctx)
	if err != nil {
		c.Set("Cache-Control", "public, max-age=60")
		return
	}

	depth := chaintip.Height - blockHeight

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
