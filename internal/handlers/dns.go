package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/go-ordfs-server/internal/cache"
	"github.com/shruggr/go-ordfs-server/internal/ordinals"
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
			Seq     int    `json:"seq"`
		}
		if err := json.Unmarshal([]byte(cached), &dnsRecord); err == nil {
			return dnsRecord.Pointer, dnsRecord.Seq, nil
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
			seq := -1

			if len(parts) > 1 {
				if s, err := strconv.Atoi(parts[1]); err == nil {
					seq = s
				}
			}

			dnsRecord := struct {
				Pointer string `json:"pointer"`
				Seq     int    `json:"seq"`
			}{
				Pointer: pointer,
				Seq:     seq,
			}
			if cachedBytes, err := json.Marshal(dnsRecord); err == nil {
				h.cache.Set(ctx, cacheKey, cachedBytes, 5*time.Minute)
			}

			return pointer, seq, nil
		}
	}

	return "", 0, fmt.Errorf("no ordfs pointer found in DNS")
}

func (h *DNSHandler) GetRoot(c *fiber.Ctx) error {
	ctx := context.Background()
	hostname := c.Hostname()

	slog.Debug("DNS GetRoot",
		"hostname", hostname,
		"ordfsHost", h.ordfsHost,
		"ordfsEnabled", h.ordfsEnabled,
		"path", c.Path())

	if !h.ordfsEnabled || hostname == h.ordfsHost {
		slog.Debug("Rendering frontend index", "hostname", hostname)
		return h.frontendHandler.RenderIndex(c)
	}

	pointer, seq, err := h.loadPointerFromDNS(hostname)
	if err != nil {
		slog.Debug("DNS lookup failed", "hostname", hostname, "error", err)
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "no ordfs configuration found for domain",
		})
	}

	slog.Debug("DNS pointer found", "hostname", hostname, "pointer", pointer, "seq", seq)

	outpoint, err := transaction.OutpointFromString(pointer)
	if err != nil {
		slog.Error("Invalid pointer in DNS", "pointer", pointer, "error", err)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid pointer in DNS",
		})
	}

	resp, err := h.contentHandler.loadContentByOutpoint(ctx, outpoint, seq, false)
	if err != nil {
		if errors.Is(err, txloader.ErrNotFound) {
			slog.Debug("Inscription not found", "outpoint", outpoint.OrdinalString())
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "inscription not found",
			})
		}
		slog.Error("Failed to load content", "outpoint", outpoint.OrdinalString(), "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	slog.Debug("Content loaded", "outpoint", outpoint.OrdinalString(), "contentType", resp.ContentType, "size", len(resp.Content))

	if resp.ContentType == "ord-fs/json" && c.Query("raw") == "" {
		redirectURL := "/index.html"
		slog.Debug("Redirecting to index.html", "from", c.Path(), "to", redirectURL, "contentType", resp.ContentType)
		return c.Redirect(redirectURL)
	}

	c.Set("Content-Type", resp.ContentType)
	c.Set("X-Outpoint", resp.Outpoint.OrdinalString())
	return c.Send(resp.Content)
}

func (h *DNSHandler) GetFileOrPointer(c *fiber.Ctx) error {
	ctx := context.Background()
	fileOrPointer := c.Params("fileOrPointer")
	hostname := c.Hostname()

	slog.Debug("DNS GetFileOrPointer",
		"hostname", hostname,
		"fileOrPointer", fileOrPointer,
		"path", c.Path())

	if fileOrPointer == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "parameter required",
		})
	}

	var resp *ordinals.ContentResponse
	var err error

	if len(fileOrPointer) == 64 {
		outpoint := &transaction.Outpoint{}
		outpoint, parseErr := transaction.OutpointFromString(fileOrPointer + "_0")
		if parseErr == nil {
			slog.Debug("Attempting to load as txid", "fileOrPointer", fileOrPointer)
			resp, err = h.contentHandler.loadContentByOutpoint(ctx, outpoint, -1, false)
		}
	} else {
		outpoint, parseErr := transaction.OutpointFromString(fileOrPointer)
		if parseErr == nil {
			slog.Debug("Attempting to load as outpoint", "fileOrPointer", fileOrPointer)
			resp, err = h.contentHandler.loadContentByOutpoint(ctx, outpoint, -1, false)
		}
	}

	if err == nil && resp != nil {
		slog.Debug("Content loaded directly", "contentType", resp.ContentType, "size", len(resp.Content))
		if resp.ContentType == "ord-fs/json" && c.Query("raw") == "" {
			redirectURL := fmt.Sprintf("/%s/index.html", fileOrPointer)
			slog.Debug("Redirecting to directory index", "from", c.Path(), "to", redirectURL)
			return c.Redirect(redirectURL)
		}

		c.Set("Content-Type", resp.ContentType)
		c.Set("X-Outpoint", resp.Outpoint.OrdinalString())
		return c.Send(resp.Content)
	}

	slog.Debug("Direct content load failed, trying DNS resolution", "hostname", hostname, "error", err)

	if !h.ordfsEnabled || hostname == h.ordfsHost {
		slog.Debug("DNS routing disabled or canonical host", "hostname", hostname)
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "not found",
		})
	}

	pointer, seq, dnsErr := h.loadPointerFromDNS(hostname)
	if dnsErr != nil {
		slog.Debug("DNS lookup failed for file resolution", "hostname", hostname, "error", dnsErr)
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "not found",
		})
	}

	slog.Debug("Loading directory from DNS pointer", "pointer", pointer, "seq", seq)

	dirOutpoint, err := transaction.OutpointFromString(pointer)
	if err != nil {
		slog.Error("Invalid directory pointer in DNS", "pointer", pointer, "error", err)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid pointer in DNS",
		})
	}

	dirResp, err := h.contentHandler.loadContentByOutpoint(ctx, dirOutpoint, seq, false)
	if err != nil {
		slog.Debug("Directory not found", "outpoint", dirOutpoint.OrdinalString(), "error", err)
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "directory not found",
		})
	}

	var directory map[string]string
	if err := json.Unmarshal(dirResp.Content, &directory); err != nil {
		slog.Error("Invalid directory format", "outpoint", dirOutpoint.OrdinalString(), "error", err)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid directory format",
		})
	}

	slog.Debug("Directory loaded", "fileCount", len(directory), "requestedFile", fileOrPointer)

	filePointer, exists := directory[fileOrPointer]
	if !exists {
		slog.Debug("File not found in directory", "file", fileOrPointer, "availableFiles", len(directory))
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "file not found",
		})
	}

	filePointer = strings.TrimPrefix(filePointer, "ord://")
	fileOutpoint, err := transaction.OutpointFromString(filePointer)
	if err != nil {
		slog.Error("Invalid file pointer in directory", "filePointer", filePointer, "error", err)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid file pointer",
		})
	}

	slog.Debug("Loading file from directory", "file", fileOrPointer, "outpoint", fileOutpoint.OrdinalString())

	fileResp, err := h.contentHandler.loadContentByOutpoint(ctx, fileOutpoint, -1, false)
	if err != nil {
		if errors.Is(err, txloader.ErrNotFound) {
			slog.Debug("File content not found", "outpoint", fileOutpoint.OrdinalString())
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "file not found",
			})
		}
		slog.Error("Failed to load file content", "outpoint", fileOutpoint.OrdinalString(), "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	slog.Debug("File loaded successfully", "contentType", fileResp.ContentType, "size", len(fileResp.Content))

	c.Set("Content-Type", fileResp.ContentType)
	c.Set("X-Outpoint", fileResp.Outpoint.OrdinalString())
	return c.Send(fileResp.Content)
}
