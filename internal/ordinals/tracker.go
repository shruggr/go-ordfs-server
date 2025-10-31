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

type ContentResponse struct {
	Outpoint    *transaction.Outpoint
	ContentType string
	Content     []byte
	MergedMap   map[string]string
	Sequence    int
	Output      []byte
}

func (t *Tracker) loadMergedMap(ctx context.Context, origin *transaction.Outpoint, maxScore int) map[string]string {
	mapKey := fmt.Sprintf("map:%s", origin.OrdinalString())

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

func (t *Tracker) Resolve(ctx context.Context, origin *transaction.Outpoint, seq int, includeMap bool) (*ContentResponse, error) {
	slog.Debug("Resolve started",
		"origin", origin.OrdinalString(),
		"seq", seq,
		"includeMap", includeMap)

	spendsKey := fmt.Sprintf("seq:%s", origin.OrdinalString())
	versionsKey := fmt.Sprintf("rev:%s", origin.OrdinalString())
	mapKey := fmt.Sprintf("map:%s", origin.OrdinalString())

	// Step 1: Determine target sequence
	targetSeq := seq
	if seq == -1 {
		// For -1, we need to crawl to the end
		targetSeq = -1
	}

	lastRevision := &ContentResponse{
		MergedMap: make(map[string]string),
	}
	// Step 2: Check if we have the outpoint at the target sequence in cache
	if targetSeq >= 0 {
		seqMembers := t.cache.ZRangeByScore(ctx, spendsKey, &redis.ZRangeBy{
			Min:   fmt.Sprintf("%d", targetSeq),
			Max:   fmt.Sprintf("%d", targetSeq),
			Count: 1,
		}).Val()
		if len(seqMembers) > 0 {
			lastRevision.Outpoint, _ = transaction.OutpointFromString(seqMembers[0])
			lastRevision.Sequence = targetSeq
			slog.Debug("Found cached sequence", "seq", targetSeq, "outpoint", lastRevision.Outpoint.OrdinalString())
		}
	}

	// Step 3: If we need to crawl (no cache hit or seq=-1)
	var crawlStartSeq int = -1
	var crawlStartOutpoint *transaction.Outpoint

	if targetSeq == -1 || lastRevision.Outpoint == nil {
		// Prime the loop with both transaction and outpoint
		var currentTx *transaction.Transaction

		lastSpendMembers := t.cache.ZRevRangeWithScores(ctx, spendsKey, 0, 0).Val()
		if len(lastSpendMembers) > 0 {
			// Resume from last cached position - get its spend
			crawlStartSeq = int(lastSpendMembers[0].Score)
			crawlStartOutpoint, _ = transaction.OutpointFromString(lastSpendMembers[0].Member.(string))
			slog.Debug("Resuming from cached position", "seq", crawlStartSeq, "outpoint", crawlStartOutpoint.OrdinalString())

			spendTxid, err := t.txLoader.GetSpend(ctx, crawlStartOutpoint.OrdinalString())
			if err != nil {
				return nil, fmt.Errorf("failed to get spend: %w", err)
			}
			slog.Debug("Got spend txid", "spendTxid", spendTxid)
			if spendTxid != nil {
				currentTx, err = t.txLoader.LoadTx(ctx, spendTxid.String())
				if err != nil {
					return nil, fmt.Errorf("failed to load spending tx: %w", err)
				}
				slog.Debug("Loaded spending tx", "txid", currentTx.TxID().String(), "outputs", len(currentTx.Outputs))

				lastRevision.Outpoint, err = t.calculateOrdinalOutput(ctx, currentTx, crawlStartOutpoint)
				if err != nil {
					return nil, fmt.Errorf("failed to calculate ordinal output: %w", err)
				}
				slog.Debug("Calculated next position", "outpoint", lastRevision.Outpoint.OrdinalString(), "nextSeq", crawlStartSeq+1)
				lastRevision.Sequence = crawlStartSeq + 1
			} else {
				// End of chain - use the cached outpoint as final position
				lastRevision.Outpoint = crawlStartOutpoint
				lastRevision.Sequence = crawlStartSeq
				slog.Debug("End of chain at cached position", "seq", crawlStartSeq, "outpoint", crawlStartOutpoint.OrdinalString())
			}
		} else {
			// Starting from origin
			crawlStartSeq = -1
			lastRevision.Outpoint = origin
			lastRevision.Sequence = 0
			slog.Debug("Starting crawl from origin", "outpoint", origin.OrdinalString())

			var err error
			currentTx, err = t.txLoader.LoadTx(ctx, lastRevision.Outpoint.Txid.String())
			if err != nil {
				return nil, fmt.Errorf("failed to load origin tx: %w", err)
			}
		}

		// Step 3a: Crawl forward
		slog.Debug("Entering crawl loop", "currentTx", currentTx != nil, "lastRevision.Outpoint", lastRevision.Outpoint.OrdinalString(), "seq", lastRevision.Sequence)
		for currentTx != nil && lastRevision.Outpoint != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			// Parse current transaction at outpoint
			slog.Debug("Crawling", "seq", lastRevision.Sequence, "outpoint", lastRevision.Outpoint.OrdinalString(), "txid", currentTx.TxID().String())

			if int(lastRevision.Outpoint.Index) >= len(currentTx.Outputs) {
				return nil, fmt.Errorf("invalid outpoint index")
			}

			output := currentTx.Outputs[lastRevision.Outpoint.Index]
			scriptData := t.parseScript(output.LockingScript)

			lastRevision.Output = output.Bytes()

			// Cache in seq:
			t.cache.ZAdd(ctx, spendsKey, redis.Z{
				Score:  float64(lastRevision.Sequence),
				Member: lastRevision.Outpoint.OrdinalString(),
			})

			// If has content, update lastRevision and cache in rev:
			if scriptData.Content != nil {
				t.cache.ZAdd(ctx, versionsKey, redis.Z{
					Score:  float64(lastRevision.Sequence),
					Member: lastRevision.Outpoint.OrdinalString(),
				})

				lastRevision.Content = scriptData.Content
				lastRevision.ContentType = scriptData.ContentType
			}

			// If has MAP, merge and cache in map:
			if len(scriptData.MapData) > 0 {
				t.cache.ZAdd(ctx, mapKey, redis.Z{
					Score:  float64(lastRevision.Sequence),
					Member: lastRevision.Outpoint.OrdinalString(),
				})

				if includeMap {
					for k, v := range scriptData.MapData {
						lastRevision.MergedMap[k] = v
					}
				}
			}

			// Check if we've reached the target sequence
			if targetSeq >= 0 && lastRevision.Sequence >= targetSeq {
				break
			}

			// Check for existing cached chain to merge
			nextSpendKey := fmt.Sprintf("seq:%s", lastRevision.Outpoint.OrdinalString())
			if t.cache.ZCard(ctx, nextSpendKey).Val() > 1 {
				// Found intersection with existing chain (more than just the current entry), merge it
				mergedOutpoint, mergedSeq, err := t.mergeExistingChain(ctx, origin, lastRevision.Outpoint, lastRevision.Sequence)
				if err != nil {
					return nil, fmt.Errorf("failed to merge existing chain: %w", err)
				}

				// Load MAP data from the merged range if needed
				if includeMap {
					mergedMaps := t.loadMergedMap(ctx, lastRevision.Outpoint, mergedSeq-lastRevision.Sequence)
					for k, v := range mergedMaps {
						lastRevision.MergedMap[k] = v
					}
				}

				// Check if there are any inscriptions in the merged range
				if t.cache.ZCard(ctx, fmt.Sprintf("rev:%s", lastRevision.Outpoint.OrdinalString())).Val() > 0 {
					// Clear content so it gets loaded from cache after the loop
					lastRevision.Content = nil
					lastRevision.ContentType = ""
				}

				// If we found the target in the merged chain, we're done
				if targetSeq >= 0 && mergedSeq >= targetSeq {
					break
				}

				// Otherwise, we've jumped ahead
				lastRevision.Outpoint = mergedOutpoint
				lastRevision.Sequence = mergedSeq
			}

			// Get the next spend to continue crawling
			spendTxid, err := t.txLoader.GetSpend(ctx, lastRevision.Outpoint.OrdinalString())
			if err != nil {
				return nil, fmt.Errorf("failed to get spend: %w", err)
			}
			if spendTxid == nil {
				// End of chain
				break
			}

			currentTx, err = t.txLoader.LoadTx(ctx, spendTxid.String())
			if err != nil {
				return nil, fmt.Errorf("failed to load spending tx: %w", err)
			}

			lastRevision.Outpoint, err = t.calculateOrdinalOutput(ctx, currentTx, lastRevision.Outpoint)
			if err != nil {
				return nil, fmt.Errorf("failed to calculate ordinal output: %w", err)
			}

			// Increment sequence for next iteration
			lastRevision.Sequence++
		}

		// If seq=-1, the last outpoint we crawled to is our target
		if targetSeq == -1 && lastRevision.Sequence >= 0 {
			targetSeq = lastRevision.Sequence
		}
	}

	// Step 4: Build response
	// If we crawled and found content, lastRevision already has it
	if lastRevision.Content == nil {
		// Load last revision from cache
		revMembers := t.cache.ZRevRangeByScoreWithScores(ctx, versionsKey, &redis.ZRangeBy{
			Min: "0",
			Max: fmt.Sprintf("%d", targetSeq),
		}).Val()

		if len(revMembers) == 0 {
			return nil, fmt.Errorf("no inscription found: %w", txloader.ErrNotFound)
		}

		revOutpoint, _ := transaction.OutpointFromString(revMembers[0].Member.(string))

		tx, err := t.txLoader.LoadTx(ctx, revOutpoint.Txid.String())
		if err != nil {
			return nil, fmt.Errorf("failed to load tx: %w", err)
		}

		if int(revOutpoint.Index) >= len(tx.Outputs) {
			return nil, fmt.Errorf("invalid outpoint index")
		}

		output := tx.Outputs[revOutpoint.Index]
		scriptData := t.parseScript(output.LockingScript)

		lastRevision.Content = scriptData.Content
		lastRevision.ContentType = scriptData.ContentType

		// If revision is at the same outpoint as current position, use the same output
		if lastRevision.Outpoint != nil && *revOutpoint == *lastRevision.Outpoint {
			lastRevision.Output = output.Bytes()
		}
	}

	// Load output from current position if not already set
	if lastRevision.Output == nil && lastRevision.Outpoint != nil {
		tx, err := t.txLoader.LoadTx(ctx, lastRevision.Outpoint.Txid.String())
		if err != nil {
			return nil, fmt.Errorf("failed to load tx for output: %w", err)
		}
		if int(lastRevision.Outpoint.Index) >= len(tx.Outputs) {
			return nil, fmt.Errorf("invalid outpoint index")
		}
		lastRevision.Output = tx.Outputs[lastRevision.Outpoint.Index].Bytes()
	}

	// Load MAP data if needed
	if includeMap {
		// Load all MAP entries from before we started crawling
		preCrawlMaps := t.loadMergedMap(ctx, origin, crawlStartSeq)

		// Merge with what we accumulated during crawl
		if len(preCrawlMaps) > 0 {
			if lastRevision.MergedMap == nil {
				lastRevision.MergedMap = make(map[string]string)
			}
			// Pre-crawl maps go first, then we overlay crawled maps on top
			for k, v := range preCrawlMaps {
				if _, exists := lastRevision.MergedMap[k]; !exists {
					lastRevision.MergedMap[k] = v
				}
			}
		}
	}

	return lastRevision, nil
}

func (t *Tracker) mergeExistingChain(ctx context.Context, origin *transaction.Outpoint, intersectionOutpoint *transaction.Outpoint, currentSeq int) (*transaction.Outpoint, int, error) {
	intersectionSpendsKey := fmt.Sprintf("seq:%s", intersectionOutpoint.OrdinalString())
	existingSpends := t.cache.ZRangeWithScores(ctx, intersectionSpendsKey, 0, -1).Val()

	if len(existingSpends) == 0 {
		return intersectionOutpoint, currentSeq, nil
	}

	originSpendsKey := fmt.Sprintf("seq:%s", origin.OrdinalString())
	originVersionsKey := fmt.Sprintf("rev:%s", origin.OrdinalString())
	originMapKey := fmt.Sprintf("map:%s", origin.OrdinalString())

	// Merge seq: data
	for _, member := range existingSpends {
		outpointStr := member.Member.(string)
		originalScore := int(member.Score)
		newScore := currentSeq + originalScore

		t.cache.ZAdd(ctx, originSpendsKey, redis.Z{
			Score:  float64(newScore),
			Member: outpointStr,
		})
	}

	// Merge rev: data
	intersectionVersionsKey := fmt.Sprintf("rev:%s", intersectionOutpoint.OrdinalString())
	existingVersions := t.cache.ZRangeWithScores(ctx, intersectionVersionsKey, 0, -1).Val()

	for _, member := range existingVersions {
		outpointStr := member.Member.(string)
		originalScore := int(member.Score)
		newScore := currentSeq + originalScore + 1

		t.cache.ZAdd(ctx, originVersionsKey, redis.Z{
			Score:  float64(newScore),
			Member: outpointStr,
		})
	}

	// Merge map: data
	intersectionMapKey := fmt.Sprintf("map:%s", intersectionOutpoint.OrdinalString())
	existingMaps := t.cache.ZRangeWithScores(ctx, intersectionMapKey, 0, -1).Val()

	for _, member := range existingMaps {
		outpointStr := member.Member.(string)
		originalScore := int(member.Score)
		newScore := currentSeq + originalScore

		t.cache.ZAdd(ctx, originMapKey, redis.Z{
			Score:  float64(newScore),
			Member: outpointStr,
		})
	}

	// Return the last outpoint in the merged chain
	lastOutpointStr := existingSpends[len(existingSpends)-1].Member.(string)
	lastOutpoint, err := transaction.OutpointFromString(lastOutpointStr)
	if err != nil {
		return intersectionOutpoint, currentSeq, err
	}

	newSequence := currentSeq + len(existingSpends)

	return lastOutpoint, newSequence, nil
}
