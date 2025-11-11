package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/gofiber/fiber/v2"
	"github.com/shruggr/go-ordfs-server/loader"
	"github.com/shruggr/go-ordfs-server/ordfs"
)

func sendContentResponse(c *fiber.Ctx, resp *ordfs.Response, seq *int) error {
	slog.Debug("sendContentResponse called",
		"contentType", resp.ContentType,
		"outpoint", resp.Outpoint.OrdinalString(),
		"seq", seq,
		"map", resp.Map)

	c.Set("Content-Type", resp.ContentType)
	c.Set("X-Outpoint", resp.Outpoint.OrdinalString())

	if resp.Origin != nil {
		c.Set("X-Origin", resp.Origin.OrdinalString())
	}
	c.Set("X-Ord-Seq", fmt.Sprintf("%d", resp.Sequence))

	if seq == nil {
		c.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	} else {
		c.Set("Cache-Control", "public, max-age=86400, immutable")
	}

	if resp.Map != "" {
		c.Set("X-Map", resp.Map)
	}

	if resp.Parent != nil {
		c.Set("X-Parent", resp.Parent.OrdinalString())
	}

	if c.QueryBool("out", false) && len(resp.Output) > 0 {
		c.Set("X-Output", base64.StdEncoding.EncodeToString(resp.Output))
	}

	if c.Method() == fiber.MethodHead {
		if resp.ContentLength > 0 {
			c.Set("Content-Length", fmt.Sprintf("%d", resp.ContentLength))
		}
		return nil
	}

	return c.Send(resp.Content)
}

// PointerPath represents a parsed pointer path with optional seq and file path
type PointerPath struct {
	Pointer  string // raw pointer string (txid or outpoint, without seq)
	Seq      *int   // sequence number (nil if not specified)
	FilePath string // remaining path after pointer (empty if none)
}

// ParsePointerPath parses a URL path to extract pointer, optional seq, and file path
// Format: /prefix/pointer[:seq][/file/path]
// Examples:
//   - /content/abc123_0 -> {Pointer: "abc123_0", Seq: -1, FilePath: ""}
//   - /content/abc123_0:5 -> {Pointer: "abc123_0", Seq: 5, FilePath: ""}
//   - /content/abc123_0:5/style.css -> {Pointer: "abc123_0", Seq: 5, FilePath: "style.css"}
//   - /abc123 -> {Pointer: "abc123", Seq: -1, FilePath: ""}
func ParsePointerPath(path string, prefixToStrip string) (*PointerPath, error) {
	// Strip the prefix (e.g., "/content")
	path = strings.TrimPrefix(path, prefixToStrip)
	path = strings.TrimPrefix(path, "/")

	if path == "" {
		return nil, fmt.Errorf("empty path")
	}

	// Split into segments
	segments := strings.Split(path, "/")
	if len(segments) == 0 {
		return nil, fmt.Errorf("no segments in path")
	}

	// First segment is pointer[:seq]
	pointerWithSeq := segments[0]

	// Parse pointer and optional seq
	parts := strings.SplitN(pointerWithSeq, ":", 2)
	pointer := parts[0]
	var seq *int

	if len(parts) > 1 {
		seqVal, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid seq value: %s", parts[1])
		}
		seq = &seqVal
	}

	// Remaining segments form the file path
	filePath := ""
	if len(segments) > 1 {
		filePath = strings.Join(segments[1:], "/")
	}

	return &PointerPath{
		Pointer:  pointer,
		Seq:      seq,
		FilePath: filePath,
	}, nil
}

// ResolvePointerToOutpoint attempts to parse pointer as either txid or outpoint
// Returns outpoint and whether it was a txid (needs _0 appended)
func ResolvePointerToOutpoint(pointer string) (*transaction.Outpoint, bool, error) {
	// Try as outpoint first
	if strings.Contains(pointer, "_") || strings.Contains(pointer, ".") {
		outpoint, err := transaction.OutpointFromString(pointer)
		if err == nil {
			return outpoint, false, nil
		}
	}

	// Try as txid (64 hex chars)
	if len(pointer) == 64 {
		txHash, err := chainhash.NewHashFromHex(pointer)
		if err != nil {
			return nil, false, fmt.Errorf("invalid txid or outpoint: %w", err)
		}
		outpoint := &transaction.Outpoint{
			Txid:  *txHash,
			Index: 0,
		}
		return outpoint, true, nil
	}

	return nil, false, fmt.Errorf("invalid pointer format")
}

// DirectoryResolver handles loading content from directories with SPA fallback
type DirectoryResolver struct {
	ordfs *ordfs.Ordfs
}

// NewDirectoryResolver creates a new directory resolver
func NewDirectoryResolver(ordfsInstance *ordfs.Ordfs) *DirectoryResolver {
	return &DirectoryResolver{
		ordfs: ordfsInstance,
	}
}

// Resolve loads content at the pointer and resolves file paths with SPA fallback
// If the content is not a directory, it returns the content directly (ignoring filePath)
// If the content is a directory:
//   - If filePath is empty and raw=false, redirects to index.html
//   - If filePath matches a file in directory, loads and returns that file
//   - If filePath doesn't match (SPA fallback), loads and returns index.html
func (r *DirectoryResolver) Resolve(ctx context.Context, c *fiber.Ctx, pointer string, seq *int, filePath string) error {
	slog.Debug("DirectoryResolver.Resolve started",
		"pointer", pointer,
		"seq", seq,
		"filePath", filePath)

	// Determine if pointer is txid or outpoint
	outpoint, isTxid, err := ResolvePointerToOutpoint(pointer)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("invalid pointer: %v", err),
		})
	}

	slog.Debug("Resolved pointer to outpoint",
		"outpoint", outpoint.OrdinalString(),
		"isTxid", isTxid)

	// seq is already a pointer, use it directly

	// Load the content at pointer
	var resp *ordfs.Response
	if isTxid {
		req := &ordfs.Request{
			Txid:    &outpoint.Txid,
			Seq:     seq,
			Content: c.QueryBool("content", true),
			Map:     c.QueryBool("map", false),
			Output:  c.QueryBool("out", false),
			Parent:  c.QueryBool("parent", false),
		}
		slog.Debug("Loading content by txid", "request", req)
		resp, err = r.ordfs.Load(ctx, req)
	} else {
		req := &ordfs.Request{
			Outpoint: outpoint,
			Seq:      seq,
			Content:  c.QueryBool("content", true),
			Map:      c.QueryBool("map", false),
			Output:   c.QueryBool("out", false),
			Parent:   c.QueryBool("parent", false),
		}
		slog.Debug("Loading content by outpoint", "request", req)
		resp, err = r.ordfs.Load(ctx, req)
	}

	if err != nil {
		slog.Error("Failed to load content", "error", err)
		if errors.Is(err, loader.ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "inscription not found",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	slog.Debug("Content loaded successfully",
		"contentType", resp.ContentType,
		"outpoint", resp.Outpoint.OrdinalString())

	// If not a directory, serve content directly (ignore file path)
	if resp.ContentType != "ord-fs/json" {
		return sendContentResponse(c, resp, seq)
	}

	// It's a directory - handle directory logic
	var directory map[string]string
	if err := json.Unmarshal(resp.Content, &directory); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid directory format",
		})
	}

	// No file path provided - redirect to index.html (unless raw)
	if filePath == "" {
		if c.Query("raw") != "" {
			return sendContentResponse(c, resp, seq)
		}
		// Construct redirect URL with pointer
		redirectURL := fmt.Sprintf("%s/index.html", c.Path())
		return c.Redirect(redirectURL)
	}

	// Check if file exists in directory
	filePointer, exists := directory[filePath]

	// If file doesn't exist, fall back to index.html for SPA routing
	if !exists {
		filePointer, exists = directory["index.html"]
		if !exists {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "file not found and no index.html",
			})
		}
	}

	// Load the file
	filePointer = strings.TrimPrefix(filePointer, "ord://")
	fileOutpoint, isTxid, err := ResolvePointerToOutpoint(filePointer)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("invalid file pointer: %v", err),
		})
	}

	var fileResp *ordfs.Response
	if isTxid {
		req := &ordfs.Request{
			Txid:    &fileOutpoint.Txid,
			Content: c.QueryBool("content", true),
			Map:     c.QueryBool("map", false),
			Output:  c.QueryBool("out", false),
		}
		fileResp, err = r.ordfs.Load(ctx, req)
	} else {
		req := &ordfs.Request{
			Outpoint: fileOutpoint,
			Content:  c.QueryBool("content", true),
			Map:      c.QueryBool("map", false),
			Output:   c.QueryBool("out", false),
		}
		fileResp, err = r.ordfs.Load(ctx, req)
	}

	if err != nil {
		if errors.Is(err, loader.ErrNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "file not found",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	return sendContentResponse(c, fileResp, nil)
}
