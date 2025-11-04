package handlers

import (
	"context"

	"github.com/gofiber/fiber/v2"
	"github.com/shruggr/go-ordfs-server/loader"
)

type TxHandler struct {
	loader loader.Loader
}

func NewTxHandler(ldr loader.Loader) *TxHandler {
	return &TxHandler{
		loader: ldr,
	}
}

func (h *TxHandler) GetRawTx(c *fiber.Ctx) error {
	ctx := context.Background()
	txid := c.Params("txid")

	if txid == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "txid required",
		})
	}

	tx, err := h.loader.LoadTx(ctx, txid)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	c.Set("Content-Type", "application/octet-stream")
	return c.Send(tx.Bytes())
}
