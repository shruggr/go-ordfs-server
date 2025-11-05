package ordfs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/bitcoin-sv/go-templates/template/bitcom"
	"github.com/bitcoin-sv/go-templates/template/inscription"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
)

// parseOutput parses a transaction output for inscription or B protocol content
// Returns a Response with ContentType, Content, ContentSize, and Map populated
// If loadContent is false, only metadata (contentType, contentLength, map) is loaded from cache
func (o *Ordfs) parseOutput(ctx context.Context, outpoint *transaction.Outpoint, output *transaction.TransactionOutput, loadContent bool) *Response {
	cacheKey := fmt.Sprintf("parsed:%s", outpoint.OrdinalString())

	// Check cache first
	if loadContent {
		cached := o.cache.HGetAll(ctx, cacheKey).Val()
		if len(cached) > 0 {
			contentType := cached["contentType"]
			content := []byte(cached["content"])
			mapJSON := cached["map"]
			var parent *transaction.Outpoint
			if cached["parent"] != "" {
				parent, _ = transaction.OutpointFromString(cached["parent"])
			}
			return &Response{
				ContentType:   contentType,
				Content:       content,
				ContentLength: len(content),
				Map:           mapJSON,
				Parent:        parent,
			}
		}
	} else {
		// Only fetch metadata fields, not full content
		var cached map[string]string
		if err := o.cache.HMGet(ctx, cacheKey, "parsed", "contentType", "contentLength", "map", "parent").Scan(&cached); err == nil && len(cached) > 0 {
			var contentLength int
			if cached["contentLength"] != "" {
				contentLength, _ = strconv.Atoi(cached["contentLength"])
			}
			var parent *transaction.Outpoint
			if cached["parent"] != "" {
				parent, _ = transaction.OutpointFromString(cached["parent"])
			}

			return &Response{
				ContentType:   cached["contentType"],
				Content:       nil,
				ContentLength: contentLength,
				Map:           cached["map"],
				Parent:        parent,
			}
		}
	}

	lockingScript := script.Script(*output.LockingScript)

	var contentType string
	var content []byte
	var mapData map[string]string
	var parent *transaction.Outpoint

	// Try inscription first
	if insc := inscription.Decode(&lockingScript); insc != nil {
		if insc.File.Content != nil {
			contentType = insc.File.Type
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			content = insc.File.Content
		}

		// Extract parent if present
		if insc.Parent != nil {
			parent = insc.Parent
		}

		// Check for map data in inscription fields
		if mapFieldData, exists := insc.Fields[bitcom.MapPrefix]; exists {
			if mapProto := bitcom.DecodeMap(mapFieldData); mapProto != nil && mapProto.Cmd == bitcom.MapCmdSet {
				if mapData == nil {
					mapData = make(map[string]string)
				}
				for k, v := range mapProto.Data {
					mapData[k] = v
				}
			}
		}
	}

	// Try B protocol
	if bc := bitcom.Decode(&lockingScript); bc != nil {
		for _, proto := range bc.Protocols {
			if proto.Protocol == bitcom.MapPrefix {
				if mapProto := bitcom.DecodeMap(proto.Script); mapProto != nil && mapProto.Cmd == bitcom.MapCmdSet {
					if mapData == nil {
						mapData = make(map[string]string)
					}
					for k, v := range mapProto.Data {
						mapData[k] = v
					}
				}
			} else if proto.Protocol == bitcom.BPrefix {
				// B protocol content
				bProto := bitcom.DecodeB(proto.Script)
				if bProto != nil && len(bProto.Data) > 0 {
					if contentType == "" {
						contentType = string(bProto.MediaType)
						if contentType == "" {
							contentType = "application/octet-stream"
						}
					}
					if content == nil {
						content = bProto.Data
					}
				}
			}
		}
	}

	// Cache the result - always cache to mark as parsed
	cacheData := map[string]interface{}{
		"parsed": "1",
	}
	var mapJSON string
	if contentType != "" {
		cacheData["contentType"] = contentType
		cacheData["content"] = string(content)
		cacheData["contentLength"] = len(content)
	}
	if mapData != nil {
		if mapBytes, err := json.Marshal(mapData); err == nil {
			mapJSON = string(mapBytes)
			cacheData["map"] = mapJSON
		}
	}
	if parent != nil {
		cacheData["parent"] = parent.OrdinalString()
	}
	o.cache.HSet(ctx, cacheKey, cacheData)
	o.cache.Expire(ctx, cacheKey, cacheTTL)

	return &Response{
		ContentType:   contentType,
		Content:       content,
		ContentLength: len(content),
		Map:           mapJSON,
		Parent:        parent,
	}
}

// loadAndParse loads an output and parses it for content
func (o *Ordfs) loadAndParse(ctx context.Context, outpoint *transaction.Outpoint, loadContent bool) (*Response, error) {
	output, err := o.loader.LoadOutput(ctx, outpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to load output: %w", err)
	}

	return o.parseOutput(ctx, outpoint, output, loadContent), nil
}
