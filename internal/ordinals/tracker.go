package ordinals

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bitcoin-sv/go-templates/template/bitcom"
	"github.com/bitcoin-sv/go-templates/template/inscription"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/go-ordfs-server/internal/txloader"
)

type Tracker struct {
	txLoader *txloader.TxLoader
	cache    *redis.Client
}

func NewTracker(txLoader *txloader.TxLoader, cache *redis.Client) *Tracker {
	return &Tracker{
		txLoader: txLoader,
		cache:    cache,
	}
}

func (t *Tracker) ResolveSpend(ctx context.Context, outpoint *transaction.Outpoint) (*transaction.Outpoint, error) {
	cacheKey := fmt.Sprintf("spend:%s", outpoint.String())

	if cached, err := t.cache.Get(ctx, cacheKey).Result(); err == nil {
		if nextOutpoint, err := transaction.OutpointFromString(cached); err == nil {
			return nextOutpoint, nil
		}
	}

	spendTxid, err := t.txLoader.GetSpend(ctx, outpoint.String())
	if err != nil {
		return nil, err
	}

	if spendTxid == nil {
		return nil, nil
	}

	spendTx, err := t.txLoader.LoadTx(ctx, spendTxid.String())
	if err != nil {
		return nil, fmt.Errorf("failed to load spending tx: %w", err)
	}

	nextOutpoint, err := t.calculateOrdinalOutput(ctx, spendTx, outpoint)
	if err != nil {
		return nil, err
	}

	t.cache.Set(ctx, cacheKey, nextOutpoint.String(), 0)

	return nextOutpoint, nil
}

func (t *Tracker) calculateOrdinalOutput(ctx context.Context, spendTx *transaction.Transaction, spentOutpoint *transaction.Outpoint) (*transaction.Outpoint, error) {
	var inputIndex int = -1
	var ordinalOffset uint64 = 0

	for i, input := range spendTx.Inputs {
		if input.SourceTXID != nil && input.SourceTXID.Equal(spentOutpoint.Txid) && input.SourceTxOutIndex == spentOutpoint.Index {
			inputIndex = i
			break
		}

		prevTx, err := t.txLoader.LoadTx(ctx, input.SourceTXID.String())
		if err != nil {
			return nil, fmt.Errorf("failed to load input tx %s: %w", input.SourceTXID.String(), err)
		}

		if int(input.SourceTxOutIndex) >= len(prevTx.Outputs) {
			return nil, fmt.Errorf("invalid input reference")
		}

		ordinalOffset += prevTx.Outputs[input.SourceTxOutIndex].Satoshis
	}

	if inputIndex == -1 {
		return nil, fmt.Errorf("outpoint not found in spending transaction inputs")
	}

	var cumulativeSats uint64 = 0
	for i, output := range spendTx.Outputs {
		if output.Satoshis != 1 {
			continue
		}

		if cumulativeSats == ordinalOffset {
			return &transaction.Outpoint{
				Txid:  *spendTx.TxID(),
				Index: uint32(i),
			}, nil
		}
		cumulativeSats += output.Satoshis
	}

	return nil, fmt.Errorf("ordinal output not found (no 1-sat output at ordinal offset)")
}

func (t *Tracker) mergeExistingChain(ctx context.Context, origin *transaction.Outpoint, intersectionOutpoint *transaction.Outpoint, currentSequence int64, targetVersion int, currentVersionCount int64) (*transaction.Outpoint, int64, bool, error) {
	intersectionSpendsKey := fmt.Sprintf("spends:%s", intersectionOutpoint.String())
	existingSpends := t.cache.ZRangeWithScores(ctx, intersectionSpendsKey, 0, -1).Val()

	if len(existingSpends) == 0 {
		return intersectionOutpoint, currentSequence, false, nil
	}

	originSpendsKey := fmt.Sprintf("spends:%s", origin.String())
	originVersionsKey := fmt.Sprintf("versions:%s", origin.String())
	originMapKey := fmt.Sprintf("map:%s", origin.String())

	for _, member := range existingSpends {
		outpointStr := member.Member.(string)
		originalScore := int64(member.Score)
		newScore := currentSequence + originalScore

		t.cache.ZAdd(ctx, originSpendsKey, redis.Z{
			Score:  float64(newScore),
			Member: outpointStr,
		})
	}

	intersectionVersionsKey := fmt.Sprintf("versions:%s", intersectionOutpoint.String())
	existingVersions := t.cache.ZRangeWithScores(ctx, intersectionVersionsKey, 0, -1).Val()

	var targetOutpoint *transaction.Outpoint
	var targetScore int64
	foundTarget := false

	for i, member := range existingVersions {
		outpointStr := member.Member.(string)
		originalScore := int64(member.Score)
		newScore := currentSequence + originalScore + 1

		t.cache.ZAdd(ctx, originVersionsKey, redis.Z{
			Score:  float64(newScore),
			Member: outpointStr,
		})

		if targetVersion >= 0 {
			newVersionCount := currentVersionCount + int64(i) + 1
			if newVersionCount == int64(targetVersion) {
				targetOutpoint, _ = transaction.OutpointFromString(outpointStr)
				targetScore = newScore
				foundTarget = true
			}
		}
	}

	intersectionMapKey := fmt.Sprintf("map:%s", intersectionOutpoint.String())
	existingMaps := t.cache.ZRangeWithScores(ctx, intersectionMapKey, 0, -1).Val()

	for _, member := range existingMaps {
		outpointStr := member.Member.(string)
		originalScore := int64(member.Score)
		newScore := currentSequence + originalScore

		t.cache.ZAdd(ctx, originMapKey, redis.Z{
			Score:  float64(newScore),
			Member: outpointStr,
		})
	}

	if foundTarget {
		return targetOutpoint, targetScore, true, nil
	}

	lastOutpointStr := existingSpends[len(existingSpends)-1].Member.(string)
	lastOutpoint, err := transaction.OutpointFromString(lastOutpointStr)
	if err != nil {
		return intersectionOutpoint, currentSequence, false, err
	}

	newSequence := currentSequence + int64(len(existingSpends))

	return lastOutpoint, newSequence, false, nil
}

type ScriptData struct {
	ContentType string
	Content     []byte
	MapData     map[string]string
}

func (t *Tracker) parseScript(lockingScript *script.Script) *ScriptData {
	result := &ScriptData{}

	insc := inscription.Decode(lockingScript)
	if insc != nil {
		if insc.File.Content != nil {
			result.ContentType = insc.File.Type
			if result.ContentType == "" {
				result.ContentType = "application/octet-stream"
			}
			result.Content = insc.File.Content
		}

		if mapFieldData, exists := insc.Fields[bitcom.MapPrefix]; exists {
			if mapProto := bitcom.DecodeMap(mapFieldData); mapProto != nil && mapProto.Cmd == bitcom.MapCmdSet {
				if result.MapData == nil {
					result.MapData = make(map[string]string)
				}
				for k, v := range mapProto.Data {
					result.MapData[k] = v
				}
			}
		}
	}

	bc := bitcom.Decode(lockingScript)
	if bc != nil {
		for _, proto := range bc.Protocols {
			if proto.Protocol == bitcom.MapPrefix {
				if mapProto := bitcom.DecodeMap(proto.Script); mapProto != nil && mapProto.Cmd == bitcom.MapCmdSet {
					if result.MapData == nil {
						result.MapData = make(map[string]string)
					}
					for k, v := range mapProto.Data {
						result.MapData[k] = v
					}
				}
			}
		}
	}

	return result
}

type ResolveResult struct {
	Outpoint    *transaction.Outpoint
	ContentType string
	Content     []byte
	MergedMap   map[string]string
}

func (t *Tracker) loadMergedMap(ctx context.Context, origin *transaction.Outpoint, maxScore int64) map[string]string {
	mapKey := fmt.Sprintf("map:%s", origin.String())

	var mapOutpoints []string
	if maxScore < 0 {
		mapOutpoints = t.cache.ZRange(ctx, mapKey, 0, -1).Val()
	} else {
		mapOutpoints = t.cache.ZRangeByScore(ctx, mapKey, &redis.ZRangeBy{
			Min: "0",
			Max: fmt.Sprintf("%d", maxScore),
		}).Val()
	}

	mergedMap := make(map[string]string)
	for _, outpointStr := range mapOutpoints {
		outpoint, err := transaction.OutpointFromString(outpointStr)
		if err != nil {
			continue
		}

		tx, err := t.txLoader.LoadTx(ctx, outpoint.Txid.String())
		if err != nil {
			continue
		}

		if int(outpoint.Index) >= len(tx.Outputs) {
			continue
		}

		scriptData := t.parseScript(tx.Outputs[outpoint.Index].LockingScript)
		if scriptData.MapData != nil {
			for k, v := range scriptData.MapData {
				mergedMap[k] = v
			}
		}
	}

	return mergedMap
}

func (t *Tracker) Resolve(ctx context.Context, origin *transaction.Outpoint, version int, targetOutpoint *transaction.Outpoint, includeMap bool) (*ResolveResult, error) {
	slog.Debug("Resolve started",
		"origin", origin.String(),
		"version", version,
		"includeMap", includeMap)

	versionsKey := fmt.Sprintf("versions:%s", origin.String())
	spendsKey := fmt.Sprintf("spends:%s", origin.String())
	mapKey := fmt.Sprintf("map:%s", origin.String())

	var targetOut *transaction.Outpoint
	var targetRank int64 = -1

	if version >= 0 {
		versionMembers := t.cache.ZRangeWithScores(ctx, versionsKey, int64(version), int64(version)).Val()
		if len(versionMembers) > 0 {
			targetOut, _ = transaction.OutpointFromString(versionMembers[0].Member.(string))
			targetRank = int64(versionMembers[0].Score)
			slog.Debug("Found cached version", "version", version, "outpoint", targetOut.String())
		} else {
			slog.Debug("Version not in cache, will crawl", "version", version)
		}
	}

	var mergedMap map[string]string
	if includeMap {
		mergedMap = t.loadMergedMap(ctx, origin, targetRank)
	}

	if targetOut != nil {
		tx, err := t.txLoader.LoadTx(ctx, targetOut.Txid.String())
		if err != nil {
			return nil, fmt.Errorf("failed to load tx: %w", err)
		}

		if int(targetOut.Index) >= len(tx.Outputs) {
			return nil, fmt.Errorf("invalid outpoint index")
		}

		output := tx.Outputs[targetOut.Index]

		scriptData := t.parseScript(output.LockingScript)

		return &ResolveResult{
			Outpoint:    targetOut,
			ContentType: scriptData.ContentType,
			Content:     scriptData.Content,
			MergedMap:   mergedMap,
		}, nil
	}

	var currentOutpoint *transaction.Outpoint
	var currentSequence int64 = -1

	lastSpendMembers := t.cache.ZRevRangeWithScores(ctx, spendsKey, 0, 0).Val()
	if len(lastSpendMembers) > 0 {
		currentSequence = int64(lastSpendMembers[0].Score)
		currentOutpoint, _ = transaction.OutpointFromString(lastSpendMembers[0].Member.(string))
	}

	if currentOutpoint == nil {
		currentOutpoint = origin
		currentSequence = -1
	}

	var contentType string
	var content []byte
	currentVersionCount := t.cache.ZCard(ctx, versionsKey).Val()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		currentSequence++

		slog.Debug("Crawling",
			"sequence", currentSequence,
			"outpoint", currentOutpoint.String())

		tx, err := t.txLoader.LoadTx(ctx, currentOutpoint.Txid.String())
		if err != nil {
			return nil, fmt.Errorf("failed to load tx: %w", err)
		}

		if int(currentOutpoint.Index) >= len(tx.Outputs) {
			return nil, fmt.Errorf("invalid outpoint index")
		}

		output := tx.Outputs[currentOutpoint.Index]

		scriptData := t.parseScript(output.LockingScript)

		if scriptData.Content != nil {
			contentType = scriptData.ContentType
			content = scriptData.Content

			if len(scriptData.MapData) > 0 {
				t.cache.ZAdd(ctx, mapKey, redis.Z{
					Score:  float64(currentSequence),
					Member: currentOutpoint.String(),
				})
				if includeMap {
					if mergedMap == nil {
						mergedMap = make(map[string]string)
					}
					for k, v := range scriptData.MapData {
						mergedMap[k] = v
					}
				}
			}

			if version >= 0 && currentVersionCount == int64(version) {
				return &ResolveResult{
					Outpoint:    currentOutpoint,
					ContentType: contentType,
					Content:     content,
					MergedMap:   mergedMap,
				}, nil
			}

			if currentSequence > 0 {
				t.cache.ZAdd(ctx, versionsKey, redis.Z{
					Score:  float64(currentSequence),
					Member: currentOutpoint.String(),
				})
			}
			currentVersionCount++
		} else if currentSequence == 0 {
			return nil, fmt.Errorf("no inscription found at origin")
		}

		nextOutpoint, err := t.ResolveSpend(ctx, currentOutpoint)
		if err != nil {
			return nil, err
		}

		if nextOutpoint == nil {
			if version == -1 {
				return &ResolveResult{
					Outpoint:    currentOutpoint,
					ContentType: contentType,
					Content:     content,
					MergedMap:   mergedMap,
				}, nil
			}
			return nil, fmt.Errorf("version %d not found (reached end at version %d): %w", version, currentVersionCount, txloader.ErrNotFound)
		}

		t.cache.ZAdd(ctx, spendsKey, redis.Z{
			Score:  float64(currentSequence),
			Member: currentOutpoint.String(),
		})

		mergedOutpoint, mergedSequence, foundTarget, err := t.mergeExistingChain(ctx, origin, nextOutpoint, currentSequence, version, currentVersionCount)
		if err != nil {
			return nil, fmt.Errorf("failed to merge existing chain: %w", err)
		}

		if mergedOutpoint != nextOutpoint {
			if includeMap {
				chainMapData := t.loadMergedMap(ctx, nextOutpoint, mergedSequence-currentSequence-1)
				if mergedMap == nil && len(chainMapData) > 0 {
					mergedMap = make(map[string]string)
				}
				for k, v := range chainMapData {
					mergedMap[k] = v
				}
			}

			if foundTarget {
				currentOutpoint = mergedOutpoint
				currentSequence = mergedSequence
				break
			}

			currentOutpoint = mergedOutpoint
			currentSequence = mergedSequence
		} else {
			currentOutpoint = nextOutpoint
		}
	}

	tx, err := t.txLoader.LoadTx(ctx, currentOutpoint.Txid.String())
	if err != nil {
		return nil, fmt.Errorf("failed to load tx: %w", err)
	}

	if int(currentOutpoint.Index) >= len(tx.Outputs) {
		return nil, fmt.Errorf("invalid outpoint index")
	}

	scriptData := t.parseScript(tx.Outputs[currentOutpoint.Index].LockingScript)

	return &ResolveResult{
		Outpoint:    currentOutpoint,
		ContentType: scriptData.ContentType,
		Content:     scriptData.Content,
		MergedMap:   mergedMap,
	}, nil
}
