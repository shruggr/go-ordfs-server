package handlers

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/gofiber/fiber/v2"
	"github.com/shruggr/go-ordfs-server/cache"
	"github.com/shruggr/go-ordfs-server/loader"
	"github.com/shruggr/go-ordfs-server/ordfs"
)

type StreamHandler struct {
	ordfs *ordfs.Ordfs
}

func NewStreamHandler(ldr loader.Loader, redisCache *cache.RedisCache) *StreamHandler {
	ordfsInstance := ordfs.New(ldr, redisCache.Client())
	return &StreamHandler{
		ordfs: ordfsInstance,
	}
}

func (h *StreamHandler) HandleStream(c *fiber.Ctx) error {
	ctx := context.Background()

	outpointStr := c.Params("outpoint")
	if outpointStr == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "outpoint parameter required",
		})
	}

	outpoint, err := transaction.OutpointFromString(outpointStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("invalid outpoint: %v", err),
		})
	}

	var rangeStart *int64
	var rangeEnd *int64

	rangeHeader := c.Get("Range")
	if rangeHeader != "" {
		rangeStart, rangeEnd, err = parseRangeHeader(rangeHeader)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("invalid Range header: %v", err),
			})
		}
	}

	req := &ordfs.StreamRequest{
		Outpoint:   outpoint,
		RangeStart: rangeStart,
		RangeEnd:   rangeEnd,
	}

	c.Set("Accept-Ranges", "bytes")
	c.Set("Cache-Control", "no-cache")

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		resp, err := h.ordfs.StreamContent(ctx, req, w)
		if err != nil {
			c.Status(fiber.StatusInternalServerError)
			return
		}

		c.Set("Content-Type", resp.ContentType)
		c.Set("X-Origin", resp.Origin.OrdinalString())
		c.Set("X-Outpoint", req.Outpoint.OrdinalString())

		if rangeStart != nil || rangeEnd != nil {
			c.Status(fiber.StatusPartialContent)
			contentRange := fmt.Sprintf("bytes %d-%d/*",
				getOrZero(rangeStart),
				getOrZero(rangeStart)+resp.BytesWritten-1)
			c.Set("Content-Range", contentRange)
		}
	})

	return nil
}

func parseRangeHeader(rangeHeader string) (*int64, *int64, error) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return nil, nil, fmt.Errorf("invalid range header format")
	}

	rangeSpec := strings.TrimPrefix(rangeHeader, "bytes=")
	parts := strings.Split(rangeSpec, "-")

	if len(parts) != 2 {
		return nil, nil, fmt.Errorf("invalid range specification")
	}

	var start *int64
	var end *int64

	if parts[0] != "" {
		startVal, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid start value: %w", err)
		}
		start = &startVal
	}

	if parts[1] != "" {
		endVal, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid end value: %w", err)
		}
		endVal++
		end = &endVal
	}

	return start, end, nil
}

func getOrZero(val *int64) int64 {
	if val == nil {
		return 0
	}
	return *val
}
