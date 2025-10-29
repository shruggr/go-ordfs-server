package handlers

import (
	"context"
	"strconv"

	"github.com/b-open-io/overlay/headers"
	"github.com/gofiber/fiber/v2"
)

type BlockHandler struct {
	headersClient *headers.Client
}

func NewBlockHandler(headersClient *headers.Client) *BlockHandler {
	return &BlockHandler{
		headersClient: headersClient,
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

	return c.JSON(chaintip)
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
