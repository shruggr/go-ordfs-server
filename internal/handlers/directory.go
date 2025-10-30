package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/gofiber/fiber/v2"
	"github.com/shruggr/go-ordfs-server/internal/ordinals"
	"github.com/shruggr/go-ordfs-server/internal/txloader"
)

type DirectoryHandler struct {
	contentHandler *ContentHandler
}

func NewDirectoryHandler(contentHandler *ContentHandler) *DirectoryHandler {
	return &DirectoryHandler{
		contentHandler: contentHandler,
	}
}

func (h *DirectoryHandler) GetFile(c *fiber.Ctx) error {
	ctx := context.Background()
	pointer := c.Params("pointer")
	filename := c.Params("filename")

	if pointer == "" || filename == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "pointer and filename required",
		})
	}

	var dirResp *ordinals.ContentResponse
	var err error

	if len(pointer) == 64 {
		txHash, parseErr := chainhash.NewHashFromHex(pointer)
		if parseErr != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("invalid txid: %v", parseErr),
			})
		}
		dirResp, err = h.contentHandler.loadContentByTxid(ctx, txHash)
	} else {
		outpoint, parseErr := transaction.OutpointFromString(pointer)
		if parseErr != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("invalid pointer: %v", parseErr),
			})
		}
		dirResp, err = h.contentHandler.loadContentByOutpoint(ctx, outpoint, 0, false)
	}

	if err != nil {
		if errors.Is(err, txloader.ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "directory not found",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	var directory map[string]string
	if err := json.Unmarshal(dirResp.Content, &directory); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid directory format",
		})
	}

	filePointer, exists := directory[filename]
	if !exists {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "file not found in directory",
		})
	}

	filePointer = strings.TrimPrefix(filePointer, "ord://")

	var fileResp *ordinals.ContentResponse

	if len(filePointer) == 64 {
		txHash, parseErr := chainhash.NewHashFromHex(filePointer)
		if parseErr != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("invalid file txid: %v", parseErr),
			})
		}
		fileResp, err = h.contentHandler.loadContentByTxid(ctx, txHash)
	} else {
		outpoint, parseErr := transaction.OutpointFromString(filePointer)
		if parseErr != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("invalid file pointer: %v", parseErr),
			})
		}
		fileResp, err = h.contentHandler.loadContentByOutpoint(ctx, outpoint, 0, false)
	}

	if err != nil {
		if errors.Is(err, txloader.ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "file not found",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	c.Set("Content-Type", fileResp.ContentType)
	c.Set("X-Outpoint", fileResp.Outpoint.String())
	return c.Send(fileResp.Content)
}
