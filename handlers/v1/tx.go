package v1

import (
	"context"
	"regexp"

	"github.com/gofiber/fiber/v2"
	"github.com/shruggr/go-ordfs-server/loader"
)

var txidPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

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

	if !txidPattern.MatchString(txid) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid txid format (must be 64 hex characters)",
		})
	}

	tx, err := h.loader.LoadTx(ctx, txid)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	c.Set("Content-Type", "application/octet-stream")
	c.Set("Cache-Control", "public, max-age=31536000, immutable")
	return c.Send(tx.Bytes())
}
