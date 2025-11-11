package v2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/shruggr/go-ordfs-server/cache"
	"github.com/shruggr/go-ordfs-server/handlers"
	"github.com/shruggr/go-ordfs-server/loader"
	"github.com/shruggr/go-ordfs-server/ordfs"
)

type MetadataHandler struct {
	ordfs *ordfs.Ordfs
}

func NewMetadataHandler(ldr loader.Loader, redisCache *cache.RedisCache) *MetadataHandler {
	ordfsInstance := ordfs.New(ldr, redisCache.Client())
	return &MetadataHandler{
		ordfs: ordfsInstance,
	}
}

type MetadataResponse struct {
	ContentType string          `json:"contentType"`
	Outpoint    string          `json:"outpoint"`
	Origin      *string         `json:"origin,omitempty"`
	Sequence    int             `json:"sequence"`
	Map         json.RawMessage `json:"map,omitempty"`
	Parent      *string         `json:"parent,omitempty"`
	Output      *string         `json:"output,omitempty"`
}

func (h *MetadataHandler) GetMetadata(c *fiber.Ctx) error {
	ctx := context.Background()
	path := c.Params("*")

	parsed, err := handlers.ParsePointerPath(path, "")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("invalid path: %v", err),
		})
	}

	outpoint, isTxid, err := handlers.ResolvePointerToOutpoint(parsed.Pointer)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("invalid pointer: %v", err),
		})
	}

	var resp *ordfs.Response
	if isTxid {
		req := &ordfs.Request{
			Txid:    &outpoint.Txid,
			Seq:     parsed.Seq,
			Content: false,
			Map:     c.QueryBool("map", false),
			Output:  c.QueryBool("out", false),
			Parent:  c.QueryBool("parent", false),
		}
		resp, err = h.ordfs.Load(ctx, req)
	} else {
		req := &ordfs.Request{
			Outpoint: outpoint,
			Seq:      parsed.Seq,
			Content:  false,
			Map:      c.QueryBool("map", false),
			Output:   c.QueryBool("out", false),
			Parent:   c.QueryBool("parent", false),
		}
		resp, err = h.ordfs.Load(ctx, req)
	}

	if err != nil {
		if err == loader.ErrNotFound {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "inscription not found",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	metadata := MetadataResponse{
		ContentType: resp.ContentType,
		Outpoint:    resp.Outpoint.OrdinalString(),
		Sequence:    resp.Sequence,
	}

	if resp.Origin != nil {
		origin := resp.Origin.OrdinalString()
		metadata.Origin = &origin
	}

	if resp.Map != "" {
		metadata.Map = json.RawMessage(resp.Map)
	}

	if resp.Parent != nil {
		parent := resp.Parent.OrdinalString()
		metadata.Parent = &parent
	}

	if c.QueryBool("out", false) && len(resp.Output) > 0 {
		output := base64.StdEncoding.EncodeToString(resp.Output)
		metadata.Output = &output
	}

	if parsed.Seq == nil {
		c.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	} else {
		c.Set("Cache-Control", "public, max-age=86400, immutable")
	}

	return c.JSON(metadata)
}
