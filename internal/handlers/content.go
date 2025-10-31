package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bitcoin-sv/go-templates/template/bitcom"
	"github.com/bitcoin-sv/go-templates/template/inscription"
	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/go-ordfs-server/internal/cache"
	"github.com/shruggr/go-ordfs-server/internal/ordinals"
	"github.com/shruggr/go-ordfs-server/internal/txloader"
)

type ContentHandler struct {
	txLoader *txloader.TxLoader
	tracker  *ordinals.Tracker
	cache    *redis.Client
}

func NewContentHandler(txLoader *txloader.TxLoader, redisCache *cache.RedisCache) *ContentHandler {
	return &ContentHandler{
		txLoader: txLoader,
		tracker:  ordinals.NewTracker(txLoader, redisCache.Client()),
		cache:    redisCache.Client(),
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

	var resp *ordinals.ContentResponse
	var err error

	if len(txidOrOutpoint) == 64 {
		var txHash *chainhash.Hash
		txHash, err = chainhash.NewHashFromHex(txidOrOutpoint)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("invalid txid: %v", err),
			})
		}
		resp, err = h.loadContentByTxid(ctx, txHash)
	} else {
		var outpoint *transaction.Outpoint
		outpoint, err = transaction.OutpointFromString(txidOrOutpoint)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("invalid outpoint: %v", err),
			})
		}
		resp, err = h.loadContentByOutpoint(
			ctx,
			outpoint,
			c.QueryInt("seq", 0),
			c.QueryBool("map", false),
		)
	}

	if err != nil {
		if errors.Is(err, txloader.ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": err.Error(),
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	seq := c.QueryInt("seq", 0)
	return h.sendContentResponse(c, resp, seq)
}

func (h *ContentHandler) sendContentResponse(c *fiber.Ctx, resp *ordinals.ContentResponse, seq int) error {
	c.Set("Content-Type", resp.ContentType)
	c.Set("X-Outpoint", resp.Outpoint.OrdinalString())

	if resp.Origin != nil {
		c.Set("X-Origin", resp.Origin.OrdinalString())
	}
	c.Set("X-Ord-Seq", fmt.Sprintf("%d", resp.Sequence))

	if seq == -1 {
		c.Set("Cache-Control", "public, max-age=60")
	} else {
		c.Set("Cache-Control", "public, max-age=86400, immutable")
	}

	if len(resp.MergedMap) > 0 {
		if mapJSON, err := json.Marshal(resp.MergedMap); err == nil {
			c.Set("X-Map", string(mapJSON))
		}
	}

	if c.QueryBool("out", false) && len(resp.Output) > 0 {
		c.Set("X-Output", base64.StdEncoding.EncodeToString(resp.Output))
	}

	// For HEAD requests, omit the body
	if c.Method() == fiber.MethodHead {
		return nil
	}

	return c.Send(resp.Content)
}

func (h *ContentHandler) getCachedContent(ctx context.Context, cacheKey string, includeMap bool) (*ordinals.ContentResponse, bool) {
	cached := h.cache.HGetAll(ctx, cacheKey).Val()
	if len(cached) == 0 {
		return nil, false
	}

	contentType, hasType := cached["contentType"]
	content, hasContent := cached["content"]
	outpointStr, hasOutpoint := cached["outpoint"]

	if !hasType || !hasContent || !hasOutpoint {
		return nil, false
	}

	resolvedOutpoint, _ := transaction.OutpointFromString(outpointStr)

	var origin *transaction.Outpoint
	if originStr, ok := cached["origin"]; ok && originStr != "" {
		origin, _ = transaction.OutpointFromString(originStr)
	}

	sequence := 0
	if seqStr, ok := cached["sequence"]; ok {
		fmt.Sscanf(seqStr, "%d", &sequence)
	}

	response := &ordinals.ContentResponse{
		ContentType: contentType,
		Content:     []byte(content),
		Outpoint:    resolvedOutpoint,
		Origin:      origin,
		Sequence:    sequence,
		Output:      []byte(cached["output"]),
	}

	if includeMap {
		if mapJSON, ok := cached["map"]; ok && mapJSON != "" {
			var mergedMap map[string]string
			json.Unmarshal([]byte(mapJSON), &mergedMap)
			response.MergedMap = mergedMap
		}
	}

	return response, true
}

func (h *ContentHandler) cacheContentResponse(ctx context.Context, cacheKey string, cacheTTL time.Duration, response *ordinals.ContentResponse) {
	cacheData := map[string]any{
		"contentType": response.ContentType,
		"content":     response.Content,
		"outpoint":    response.Outpoint.OrdinalString(),
		"sequence":    fmt.Sprintf("%d", response.Sequence),
		"output":      response.Output,
	}

	if response.Origin != nil {
		cacheData["origin"] = response.Origin.OrdinalString()
	}

	if len(response.MergedMap) > 0 {
		if mapJSON, err := json.Marshal(response.MergedMap); err == nil {
			cacheData["map"] = string(mapJSON)
		}
	}

	h.cache.HSet(ctx, cacheKey, cacheData)
	h.cache.Expire(ctx, cacheKey, cacheTTL)
}

func (h *ContentHandler) loadContentByTxid(ctx context.Context, txHash *chainhash.Hash) (*ordinals.ContentResponse, error) {
	cacheKey := fmt.Sprintf("cache:%s:0", txHash.String())
	cacheTTL := 30 * 24 * time.Hour

	if cached, found := h.getCachedContent(ctx, cacheKey, false); found {
		return cached, nil
	}

	tx, err := h.txLoader.LoadTx(ctx, txHash.String())
	if err != nil {
		return nil, fmt.Errorf("transaction not found: %w", err)
	}

	for i, output := range tx.Outputs {
		contentType, content := h.extractContent(output)
		if content != nil {
			outpoint := &transaction.Outpoint{
				Txid:  *txHash,
				Index: uint32(i),
			}
			response := &ordinals.ContentResponse{
				ContentType: contentType,
				Content:     content,
				Outpoint:    outpoint,
				Sequence:    0,
				Output:      output.Bytes(),
			}
			h.cacheContentResponse(ctx, cacheKey, cacheTTL, response)
			return response, nil
		}
	}

	return nil, fmt.Errorf("no inscription or B protocol content found: %w", txloader.ErrNotFound)
}

func (h *ContentHandler) loadContentByOutpoint(ctx context.Context, outpoint *transaction.Outpoint, seq int, includeMap bool) (*ordinals.ContentResponse, error) {
	var cacheKey string
	var cacheTTL time.Duration

	if seq == -1 {
		cacheKey = fmt.Sprintf("cache:%s:latest", outpoint.OrdinalString())
		cacheTTL = 60 * time.Second
	} else {
		cacheKey = fmt.Sprintf("cache:%s:%d", outpoint.OrdinalString(), seq)
		cacheTTL = 30 * 24 * time.Hour
	}

	if cached, found := h.getCachedContent(ctx, cacheKey, includeMap); found {
		if includeMap && len(cached.MergedMap) == 0 {
			result, err := h.tracker.Resolve(ctx, outpoint, seq, true)
			if err == nil && len(result.MergedMap) > 0 {
				cached.MergedMap = result.MergedMap
				if mapJSON, err := json.Marshal(result.MergedMap); err == nil {
					h.cache.HSet(ctx, cacheKey, "map", string(mapJSON))
				}
			}
		}
		return cached, nil
	}

	result, err := h.tracker.Resolve(ctx, outpoint, seq, includeMap)
	if err != nil {
		return nil, err
	}

	if result.Content == nil {
		return nil, fmt.Errorf("no inscription or B protocol content found: %w", txloader.ErrNotFound)
	}

	response := &ordinals.ContentResponse{
		ContentType: result.ContentType,
		Content:     result.Content,
		Outpoint:    result.Outpoint,
		Origin:      result.Origin,
		MergedMap:   result.MergedMap,
		Sequence:    result.Sequence,
		Output:      result.Output,
	}

	h.cacheContentResponse(ctx, cacheKey, cacheTTL, response)
	return response, nil
}

func (h *ContentHandler) HandleAll(c *fiber.Ctx) error {
	ctx := context.Background()

	// Parse the path: /content/pointer[:seq][/file/path]
	parsed, err := parsePointerPath(c.Path(), "/content")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("invalid path: %v", err),
		})
	}

	// Use DirectoryResolver to handle both direct content and directory resolution
	resolver := NewDirectoryResolver(h)
	return resolver.Resolve(ctx, c, parsed.Pointer, parsed.Seq, parsed.FilePath)
}

func (h *ContentHandler) extractContent(output *transaction.TransactionOutput) (contentType string, content []byte) {
	if insc := inscription.Decode(output.LockingScript); insc != nil && insc.File.Content != nil {
		contentType = insc.File.Type
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		content = insc.File.Content
		return
	}

	bc := bitcom.Decode(output.LockingScript)
	if bc != nil && len(bc.Protocols) > 0 {
		for _, proto := range bc.Protocols {
			if proto.Protocol == bitcom.BPrefix {
				if b := bitcom.DecodeB(proto.Script); b != nil && b.Data != nil {
					contentType = string(b.MediaType)
					if contentType == "" {
						contentType = "application/octet-stream"
					}
					content = b.Data
					return
				}
			}
		}
	}

	return "", nil
}
