package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/go-ordfs-server/internal/cache"
	"github.com/shruggr/go-ordfs-server/internal/txloader"
)

type DNSHandler struct {
	contentHandler   *ContentHandler
	directoryHandler *DirectoryHandler
	frontendHandler  *FrontendHandler
	cache            *redis.Client
	ordfsHost        string
	ordfsEnabled     bool
}

func NewDNSHandler(contentHandler *ContentHandler, directoryHandler *DirectoryHandler, frontendHandler *FrontendHandler, redisCache *cache.RedisCache, ordfsHost string) *DNSHandler {
	return &DNSHandler{
		contentHandler:   contentHandler,
		directoryHandler: directoryHandler,
		frontendHandler:  frontendHandler,
		cache:            redisCache.Client(),
		ordfsHost:        ordfsHost,
		ordfsEnabled:     ordfsHost != "",
	}
}

func (h *DNSHandler) loadPointerFromDNS(hostname string) (string, int, error) {
	ctx := context.Background()
	cacheKey := fmt.Sprintf("dns:%s", hostname)

	if cached, err := h.cache.Get(ctx, cacheKey).Result(); err == nil {
		var dnsRecord struct {
			Pointer string `json:"pointer"`
			Version int    `json:"version"`
		}
		if err := json.Unmarshal([]byte(cached), &dnsRecord); err == nil {
			return dnsRecord.Pointer, dnsRecord.Version, nil
		}
	}

	lookupDomain := fmt.Sprintf("_ordfs.%s", hostname)
	records, err := net.LookupTXT(lookupDomain)
	if err != nil {
		return "", 0, fmt.Errorf("DNS lookup failed: %w", err)
	}

	prefix := "ordfs="
	for _, record := range records {
		if strings.HasPrefix(record, prefix) {
			value := strings.TrimPrefix(record, prefix)

			parts := strings.Split(value, ":")
			pointer := parts[0]
			version := 0

			if len(parts) > 1 {
				if v, err := strconv.Atoi(parts[1]); err == nil {
					version = v
				}
			}

			dnsRecord := struct {
				Pointer string `json:"pointer"`
				Version int    `json:"version"`
			}{
				Pointer: pointer,
				Version: version,
			}
			if cachedBytes, err := json.Marshal(dnsRecord); err == nil {
				h.cache.Set(ctx, cacheKey, cachedBytes, 5*time.Minute)
			}

			return pointer, version, nil
		}
	}

	return "", 0, fmt.Errorf("no ordfs pointer found in DNS")
}

func (h *DNSHandler) GetRoot(c *fiber.Ctx) error {
	ctx := context.Background()

	if !h.ordfsEnabled || c.Hostname() == h.ordfsHost {
		return h.frontendHandler.RenderIndex(c)
	}

	pointer, version, err := h.loadPointerFromDNS(c.Hostname())
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "no ordfs configuration found for domain",
		})
	}

	outpoint, err := transaction.OutpointFromString(pointer)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid pointer in DNS",
		})
	}

	resp, err := h.contentHandler.loadContentByOutpoint(ctx, outpoint, version, false)
	if err != nil {
		if errors.Is(err, txloader.ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "inscription not found",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	if resp.ContentType == "ord-fs/json" && c.Query("raw") == "" {
		return c.Redirect("/index.html")
	}

	c.Set("Content-Type", resp.ContentType)
	c.Set("X-Outpoint", resp.Outpoint.String())
	return c.Send(resp.Content)
}

func (h *DNSHandler) GetFileOrPointer(c *fiber.Ctx) error {
	ctx := context.Background()
	fileOrPointer := c.Params("fileOrPointer")

	if fileOrPointer == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "parameter required",
		})
	}

	var resp *ContentResponse
	var err error

	if len(fileOrPointer) == 64 {
		outpoint := &transaction.Outpoint{}
		outpoint, parseErr := transaction.OutpointFromString(fileOrPointer + "_0")
		if parseErr == nil {
			resp, err = h.contentHandler.loadContentByOutpoint(ctx, outpoint, 0, false)
		}
	} else {
		outpoint, parseErr := transaction.OutpointFromString(fileOrPointer)
		if parseErr == nil {
			resp, err = h.contentHandler.loadContentByOutpoint(ctx, outpoint, 0, false)
		}
	}

	if err == nil && resp != nil {
		if resp.ContentType == "ord-fs/json" && c.Query("raw") == "" {
			return c.Redirect(fmt.Sprintf("/%s/index.html", fileOrPointer))
		}

		c.Set("Content-Type", resp.ContentType)
		c.Set("X-Outpoint", resp.Outpoint.String())
		return c.Send(resp.Content)
	}

	if !h.ordfsEnabled || c.Hostname() == h.ordfsHost {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "not found",
		})
	}

	pointer, version, dnsErr := h.loadPointerFromDNS(c.Hostname())
	if dnsErr != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "not found",
		})
	}

	dirOutpoint, err := transaction.OutpointFromString(pointer)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid pointer in DNS",
		})
	}

	dirResp, err := h.contentHandler.loadContentByOutpoint(ctx, dirOutpoint, version, false)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "directory not found",
		})
	}

	var directory map[string]string
	if err := json.Unmarshal(dirResp.Content, &directory); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid directory format",
		})
	}

	filePointer, exists := directory[fileOrPointer]
	if !exists {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "file not found",
		})
	}

	filePointer = strings.TrimPrefix(filePointer, "ord://")
	fileOutpoint, err := transaction.OutpointFromString(filePointer)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid file pointer",
		})
	}

	fileResp, err := h.contentHandler.loadContentByOutpoint(ctx, fileOutpoint, 0, false)
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
