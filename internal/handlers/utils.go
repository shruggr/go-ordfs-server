package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/gofiber/fiber/v2"
	"github.com/shruggr/go-ordfs-server/internal/ordinals"
	"github.com/shruggr/go-ordfs-server/internal/txloader"
)

// PointerPath represents a parsed pointer path with optional seq and file path
type PointerPath struct {
	Pointer  string // raw pointer string (txid or outpoint, without seq)
	Seq      int    // sequence number (-1 if not specified)
	FilePath string // remaining path after pointer (empty if none)
}

// parsePointerPath parses a URL path to extract pointer, optional seq, and file path
// Format: /prefix/pointer[:seq][/file/path]
// Examples:
//   - /content/abc123_0 -> {Pointer: "abc123_0", Seq: -1, FilePath: ""}
//   - /content/abc123_0:5 -> {Pointer: "abc123_0", Seq: 5, FilePath: ""}
//   - /content/abc123_0:5/style.css -> {Pointer: "abc123_0", Seq: 5, FilePath: "style.css"}
//   - /abc123 -> {Pointer: "abc123", Seq: -1, FilePath: ""}
func parsePointerPath(path string, prefixToStrip string) (*PointerPath, error) {
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
	seq := -1

	if len(parts) > 1 {
		var err error
		seq, err = strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid seq value: %s", parts[1])
		}
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

// resolvePointerToOutpoint attempts to parse pointer as either txid or outpoint
// Returns outpoint and whether it was a txid (needs _0 appended)
func resolvePointerToOutpoint(pointer string) (*transaction.Outpoint, bool, error) {
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
	contentHandler *ContentHandler
}

// NewDirectoryResolver creates a new directory resolver
func NewDirectoryResolver(contentHandler *ContentHandler) *DirectoryResolver {
	return &DirectoryResolver{
		contentHandler: contentHandler,
	}
}

// Resolve loads content at the pointer and resolves file paths with SPA fallback
// If the content is not a directory, it returns the content directly (ignoring filePath)
// If the content is a directory:
//   - If filePath is empty and raw=false, redirects to index.html
//   - If filePath matches a file in directory, loads and returns that file
//   - If filePath doesn't match (SPA fallback), loads and returns index.html
func (r *DirectoryResolver) Resolve(ctx context.Context, c *fiber.Ctx, pointer string, seq int, filePath string) error {
	// Determine if pointer is txid or outpoint
	outpoint, isTxid, err := resolvePointerToOutpoint(pointer)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("invalid pointer: %v", err),
		})
	}

	// Override seq from query string if present
	if c.Query("seq") != "" {
		if querySeq, err := strconv.Atoi(c.Query("seq")); err == nil {
			seq = querySeq
		}
	}

	// Load the content at pointer
	var resp *ordinals.ContentResponse
	if isTxid {
		resp, err = r.contentHandler.loadContentByTxid(ctx, &outpoint.Txid)
	} else {
		resp, err = r.contentHandler.loadContentByOutpoint(ctx, outpoint, seq, c.QueryBool("map", false))
	}

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

	// If not a directory, serve content directly (ignore file path)
	if resp.ContentType != "ord-fs/json" {
		return r.contentHandler.sendContentResponse(c, resp, seq)
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
			return r.contentHandler.sendContentResponse(c, resp, seq)
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
	fileOutpoint, isTxid, err := resolvePointerToOutpoint(filePointer)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("invalid file pointer: %v", err),
		})
	}

	var fileResp *ordinals.ContentResponse
	if isTxid {
		fileResp, err = r.contentHandler.loadContentByTxid(ctx, &fileOutpoint.Txid)
	} else {
		fileResp, err = r.contentHandler.loadContentByOutpoint(ctx, fileOutpoint, -1, false)
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

	return r.contentHandler.sendContentResponse(c, fileResp, -1)
}
