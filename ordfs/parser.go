package ordfs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bitcoin-sv/go-templates/template/bitcom"
	"github.com/bitcoin-sv/go-templates/template/inscription"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/go-ordfs-server/loader"
)

// parseOutput parses a transaction output for inscription or B protocol content
func parseOutput(ctx context.Context, cache *redis.Client, outpoint *transaction.Outpoint, output *transaction.TransactionOutput) (string, []byte, map[string]string) {
	cacheKey := fmt.Sprintf("parsed:%s", outpoint.OrdinalString())

	// Check cache first
	cached := cache.HGetAll(ctx, cacheKey).Val()
	if len(cached) > 0 {
		contentType := cached["contentType"]
		content := []byte(cached["content"])
		var mapData map[string]string
		if mapJSON := cached["map"]; mapJSON != "" {
			json.Unmarshal([]byte(mapJSON), &mapData)
		}
		return contentType, content, mapData
	}

	lockingScript := script.Script(*output.LockingScript)

	var contentType string
	var content []byte
	var mapData map[string]string

	// Try inscription first
	if insc := inscription.Decode(&lockingScript); insc != nil {
		if insc.File.Content != nil {
			contentType = insc.File.Type
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			content = insc.File.Content
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

	// Cache the result if we found anything
	if contentType != "" || len(mapData) > 0 {
		cacheData := map[string]interface{}{}
		if contentType != "" {
			cacheData["contentType"] = contentType
			cacheData["content"] = string(content)
		}
		if mapData != nil {
			if mapJSON, err := json.Marshal(mapData); err == nil {
				cacheData["map"] = string(mapJSON)
			}
		}
		cache.HSet(ctx, cacheKey, cacheData)
		cache.Expire(ctx, cacheKey, cacheTTL)
	}

	return contentType, content, mapData
}

// loadAndParse loads an output and parses it for content
func loadAndParse(ctx context.Context, cache *redis.Client, ldr loader.Loader, outpoint *transaction.Outpoint) (string, []byte, map[string]string, error) {
	output, err := ldr.LoadOutput(ctx, outpoint)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to load output: %w", err)
	}

	contentType, content, mapData := parseOutput(ctx, cache, outpoint, output)
	return contentType, content, mapData, nil
}
