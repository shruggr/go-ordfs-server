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
	"github.com/shruggr/go-ordfs-server/cache"
	"github.com/shruggr/go-ordfs-server/loader"
	"github.com/shruggr/go-ordfs-server/ordfs"
)

type DNSHandler struct {
	ordfs           *ordfs.Ordfs
	resolver        *DirectoryResolver
	frontendHandler *FrontendHandler
	cache           *redis.Client
	ordfsHost       string
	ordfsEnabled    bool
}

func NewDNSHandler(ldr loader.Loader, frontendHandler *FrontendHandler, redisCache *cache.RedisCache, ordfsHost string) *DNSHandler {
	ordfsInstance := ordfs.New(ldr, redisCache.Client())
	return &DNSHandler{
		ordfs:           ordfsInstance,
		resolver:        NewDirectoryResolver(ordfsInstance),
		frontendHandler: frontendHandler,
		cache:           redisCache.Client(),
		ordfsHost:       ordfsHost,
		ordfsEnabled:    ordfsHost != "",
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

	var seqPtr *int
	if seq != -1 {
		seqPtr = &seq
	}

	resp, err := h.ordfs.Load(ctx, &ordfs.Request{
		Outpoint: outpoint,
		Seq:      seqPtr,
		Content:  c.QueryBool("content", true),
		Map:      c.QueryBool("map", false),
		Output:   c.QueryBool("out", false),
	})
	if err != nil {
		if errors.Is(err, loader.ErrNotFound) {
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

	return sendContentResponse(c, resp, seqPtr)
}

func (h *DNSHandler) HandleAll(c *fiber.Ctx) error {
	ctx := context.Background()
	path := c.Path()
	hostname := c.Hostname()

	// Special case: exact root path
	if path == "/" {
		return h.GetRoot(c)
	}

	// Try to parse path as pointer with optional seq and file path
	parsed, parseErr := parsePointerPath(path, "")

	// If successfully parsed as pointer, try direct load
	if parseErr == nil {
		_, _, err := resolvePointerToOutpoint(parsed.Pointer)
		if err == nil {
			// Valid pointer - load directly (canonical host behavior)
			return h.resolver.Resolve(ctx, c, parsed.Pointer, parsed.Seq, parsed.FilePath)
		}
	}

	// Not a valid pointer - check if on DNS domain
	if !h.ordfsEnabled || hostname == h.ordfsHost {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "not found",
		})
	}

	// Load pointer from DNS
	dnsPointer, dnsSeq, err := h.loadPointerFromDNS(hostname)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "no ordfs configuration found for domain",
		})
	}

	// Treat entire path as file path in DNS-resolved directory
	filePath := strings.TrimPrefix(path, "/")

	// Use DirectoryResolver with DNS pointer
	return h.resolver.Resolve(ctx, c, dnsPointer, &dnsSeq, filePath)
}
