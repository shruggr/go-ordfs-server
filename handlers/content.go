package handlers

import (
	"context"
	"errors"
	"fmt"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
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

func (h *ContentHandler) GetContent(c *fiber.Ctx) error {
	ctx := context.Background()
	txidOrOutpoint := c.Params("txidOrOutpoint")

	if txidOrOutpoint == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "txidOrOutpoint required",
		})
	}

	var resp *ordfs.Response
	var err error

	if len(txidOrOutpoint) == 64 {
		var txHash *chainhash.Hash
		txHash, err = chainhash.NewHashFromHex(txidOrOutpoint)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("invalid txid: %v", err),
			})
		}
		req := &ordfs.Request{
			Txid:    txHash,
			Content: true,
			Output:  true,
		}
		resp, err = h.ordfs.Load(ctx, req)
	} else {
		var outpoint *transaction.Outpoint
		outpoint, err = transaction.OutpointFromString(txidOrOutpoint)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("invalid outpoint: %v", err),
			})
		}
		resp, err = h.ordfs.Load(ctx, buildRequestFromQuery(c, outpoint))
	}

	if err != nil {
		if errors.Is(err, loader.ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": err.Error(),
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	seq := c.QueryInt("seq", 0)
	return sendContentResponse(c, resp, seq)
}

func buildRequestFromQuery(c *fiber.Ctx, outpoint *transaction.Outpoint) *ordfs.Request {
	var seq *int
	if c.Query("seq") != "" {
		s := c.QueryInt("seq", 0)
		seq = &s
	}

	return &ordfs.Request{
		Outpoint: outpoint,
		Seq:      seq,
		Content:  c.QueryBool("content", true),
		Map:      c.QueryBool("map", false),
		Output:   c.QueryBool("out", false),
	}
}

func (h *ContentHandler) HandleAll(c *fiber.Ctx) error {
	ctx := context.Background()

	parsed, err := parsePointerPath(c.Path(), "/content")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("invalid path: %v", err),
		})
	}

	return h.resolver.Resolve(ctx, c, parsed.Pointer, parsed.Seq, parsed.FilePath)
}
