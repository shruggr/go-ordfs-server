package handlers

import (
	"context"
	"regexp"
	"strconv"

	"github.com/b-open-io/overlay/headers"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/gofiber/fiber/v2"
	"github.com/shruggr/go-ordfs-server/loader"
)

var txidPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

type TxHandler struct {
	loader        loader.Loader
	headersClient *headers.Client
}

func NewTxHandler(ldr loader.Loader, headersClient *headers.Client) *TxHandler {
	return &TxHandler{
		loader:        ldr,
		headersClient: headersClient,
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

func (h *TxHandler) GetMerkleProof(c *fiber.Ctx) error {
	ctx := context.Background()
	txid := c.Params("txid")

	if !txidPattern.MatchString(txid) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid txid format (must be 64 hex characters)",
		})
	}

	proof, err := h.loader.LoadMerkleProof(ctx, txid)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	merklePath, err := transaction.NewMerklePathFromBinary(proof)
	if err == nil {
		h.setCacheHeadersByBlockHeight(c, merklePath.BlockHeight)
	} else {
		c.Set("Cache-Control", "public, max-age=60")
	}

	c.Set("Content-Type", "application/octet-stream")
	return c.Send(proof)
}

func (h *TxHandler) GetBeef(c *fiber.Ctx) error {
	ctx := context.Background()
	txid := c.Params("txid")

	if !txidPattern.MatchString(txid) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid txid format (must be 64 hex characters)",
		})
	}

	beef, err := h.loader.LoadBeef(ctx, txid)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	tx, err := transaction.NewTransactionFromBEEF(beef)
	if err == nil && tx.MerklePath != nil {
		h.setCacheHeadersByBlockHeight(c, tx.MerklePath.BlockHeight)
	} else {
		c.Set("Cache-Control", "public, max-age=60")
	}

	c.Set("Content-Type", "application/octet-stream")
	return c.Send(beef)
}

func (h *TxHandler) GetOutput(c *fiber.Ctx) error {
	ctx := context.Background()
	txid := c.Params("txid")
	outputIndex := c.Params("outputIndex")

	if !txidPattern.MatchString(txid) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid txid format (must be 64 hex characters)",
		})
	}

	_, err := strconv.ParseUint(outputIndex, 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid outputIndex (must be a valid uint32)",
		})
	}

	outpoint := txid + "_" + outputIndex
	txOutpoint, err := transaction.OutpointFromString(outpoint)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid outpoint format",
		})
	}

	output, err := h.loader.LoadOutput(ctx, txOutpoint)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	c.Set("Content-Type", "application/octet-stream")
	c.Set("Cache-Control", "public, max-age=31536000, immutable")
	return c.Send(output.Bytes())
}

func (h *TxHandler) setCacheHeadersByBlockHeight(c *fiber.Ctx, blockHeight uint32) {
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
