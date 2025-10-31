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
		if output.Satoshis == 0 {
			continue
		}

		if cumulativeSats == ordinalOffset && output.Satoshis == 1 {
			return &transaction.Outpoint{
				Txid:  *spendTx.TxID(),
				Index: uint32(i),
			}, nil
		}
		cumulativeSats += output.Satoshis
	}

	return nil, fmt.Errorf("ordinal output not found (no 1-sat output at ordinal offset)")
}

func (t *Tracker) calculatePreviousOrdinalInput(ctx context.Context, createTx *transaction.Transaction, currentOutpoint *transaction.Outpoint) (*transaction.Outpoint, error) {
	if int(currentOutpoint.Index) >= len(createTx.Outputs) {
		return nil, fmt.Errorf("invalid outpoint index")
	}

	currentOutput := createTx.Outputs[currentOutpoint.Index]
	if currentOutput.Satoshis != 1 {
		return nil, fmt.Errorf("output is not a 1-sat output")
	}

	var ordinalOffset uint64 = 0
	for i := 0; i < int(currentOutpoint.Index); i++ {
		if createTx.Outputs[i].Satoshis > 0 {
			ordinalOffset += createTx.Outputs[i].Satoshis
		}
	}

	var cumulativeSats uint64 = 0
	for _, input := range createTx.Inputs {
		prevTx, err := t.txLoader.LoadTx(ctx, input.SourceTXID.String())
		if err != nil {
			return nil, fmt.Errorf("failed to load input tx %s: %w", input.SourceTXID.String(), err)
		}

		if int(input.SourceTxOutIndex) >= len(prevTx.Outputs) {
			return nil, fmt.Errorf("invalid input reference")
		}

		prevOutputSats := prevTx.Outputs[input.SourceTxOutIndex].Satoshis

		if cumulativeSats+prevOutputSats > ordinalOffset {
			return &transaction.Outpoint{
				Txid:  *input.SourceTXID,
				Index: input.SourceTxOutIndex,
			}, nil
		}

		cumulativeSats += prevOutputSats
	}

	return nil, fmt.Errorf("could not find input containing ordinal at offset %d", ordinalOffset)
}

func (t *Tracker) isTrueOrigin(ctx context.Context, createTx *transaction.Transaction, currentOutpoint *transaction.Outpoint) (bool, error) {
	prevOutpoint, err := t.calculatePreviousOrdinalInput(ctx, createTx, currentOutpoint)
	if err != nil {
		return false, err
	}

	prevTx, err := t.txLoader.LoadTx(ctx, prevOutpoint.Txid.String())
	if err != nil {
		return false, fmt.Errorf("failed to load previous tx: %w", err)
	}

	if int(prevOutpoint.Index) >= len(prevTx.Outputs) {
		return false, fmt.Errorf("invalid previous outpoint index")
	}

	prevOutput := prevTx.Outputs[prevOutpoint.Index]
	return prevOutput.Satoshis != 1, nil
}

type ChainEntry struct {
	Outpoint    *transaction.Outpoint
	RelativeSeq int
	ScriptData  *ScriptData
}

func (t *Tracker) saveBackwardProgress(ctx context.Context, requestedOutpoint *transaction.Outpoint, chain []ChainEntry) {
	spendsKey := fmt.Sprintf("seq:%s", requestedOutpoint.OrdinalString())

	members := make([]redis.Z, len(chain))
	for i, entry := range chain {
		members[i] = redis.Z{
			Score:  float64(entry.RelativeSeq),
			Member: entry.Outpoint.OrdinalString(),
		}
	}

	t.cache.ZAdd(ctx, spendsKey, members...)
	slog.Debug("Saved backward progress", "key", spendsKey, "count", len(chain))
}

func (t *Tracker) backwardCrawl(ctx context.Context, requestedOutpoint *transaction.Outpoint) (*transaction.Outpoint, []ChainEntry, error) {
	requestedTx, err := t.txLoader.LoadTx(ctx, requestedOutpoint.Txid.String())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load requested tx: %w", err)
	}

	if int(requestedOutpoint.Index) >= len(requestedTx.Outputs) {
		return nil, nil, fmt.Errorf("invalid outpoint index")
	}

	requestedScriptData := t.parseScript(requestedTx.Outputs[requestedOutpoint.Index].LockingScript)
	chain := []ChainEntry{{Outpoint: requestedOutpoint, RelativeSeq: 0, ScriptData: requestedScriptData}}
	currentOutpoint := requestedOutpoint
	relativeSeq := 0

	slog.Debug("Starting backward crawl", "outpoint", requestedOutpoint.OrdinalString())

	for {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}

		currentTx, err := t.txLoader.LoadTx(ctx, currentOutpoint.Txid.String())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load tx %s: %w", currentOutpoint.Txid.String(), err)
		}

		isOrigin, err := t.isTrueOrigin(ctx, currentTx, currentOutpoint)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to check if true origin: %w", err)
		}

		if isOrigin {
			slog.Debug("Found true origin", "outpoint", currentOutpoint.OrdinalString(), "depth", -relativeSeq)
			return currentOutpoint, chain, nil
		}

		prevOutpoint, err := t.calculatePreviousOrdinalInput(ctx, currentTx, currentOutpoint)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to calculate previous input: %w", err)
		}

		knownOrigin := t.cache.HGet(ctx, "origins", prevOutpoint.OrdinalString()).Val()
		if knownOrigin != "" {
			slog.Debug("Found intersection with known chain", "prevOutpoint", prevOutpoint.OrdinalString(), "knownOrigin", knownOrigin)
			trueOrigin, err := transaction.OutpointFromString(knownOrigin)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to parse known origin: %w", err)
			}
			return trueOrigin, chain, nil
		}

		prevTx, err := t.txLoader.LoadTx(ctx, prevOutpoint.Txid.String())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load previous tx: %w", err)
		}

		if int(prevOutpoint.Index) >= len(prevTx.Outputs) {
			return nil, nil, fmt.Errorf("invalid previous outpoint index")
		}

		prevScriptData := t.parseScript(prevTx.Outputs[prevOutpoint.Index].LockingScript)

		relativeSeq--
		chain = append(chain, ChainEntry{Outpoint: prevOutpoint, RelativeSeq: relativeSeq, ScriptData: prevScriptData})
		currentOutpoint = prevOutpoint

		if relativeSeq%10 == 0 {
			t.saveBackwardProgress(ctx, requestedOutpoint, chain)
		}

		slog.Debug("Backward crawl step", "outpoint", currentOutpoint.OrdinalString(), "relativeSeq", relativeSeq)
	}
}

func (t *Tracker) migrateToTrueOrigin(ctx context.Context, requestedOutpoint, trueOrigin *transaction.Outpoint, chain []ChainEntry) error {
	offset := -chain[len(chain)-1].RelativeSeq

	slog.Debug("Migrating to true origin",
		"requestedOutpoint", requestedOutpoint.OrdinalString(),
		"trueOrigin", trueOrigin.OrdinalString(),
		"offset", offset,
		"chainLength", len(chain))

	pipe := t.cache.Pipeline()

	originSpendsKey := fmt.Sprintf("seq:%s", trueOrigin.OrdinalString())
	originVersionsKey := fmt.Sprintf("rev:%s", trueOrigin.OrdinalString())
	originMapKey := fmt.Sprintf("map:%s", trueOrigin.OrdinalString())

	members := make([]redis.Z, len(chain))
	originUpdates := make(map[string]interface{})

	for i, entry := range chain {
		absoluteSeq := entry.RelativeSeq + offset
		members[i] = redis.Z{
			Score:  float64(absoluteSeq),
			Member: entry.Outpoint.OrdinalString(),
		}
		originUpdates[entry.Outpoint.OrdinalString()] = trueOrigin.OrdinalString()

		if entry.ScriptData != nil {
			if entry.ScriptData.Content != nil {
				pipe.ZAdd(ctx, originVersionsKey, redis.Z{
					Score:  float64(absoluteSeq),
					Member: entry.Outpoint.OrdinalString(),
				})
			}

			if len(entry.ScriptData.MapData) > 0 {
				pipe.ZAdd(ctx, originMapKey, redis.Z{
					Score:  float64(absoluteSeq),
					Member: entry.Outpoint.OrdinalString(),
				})
			}
		}
	}

	pipe.ZAdd(ctx, originSpendsKey, members...)
	pipe.HSet(ctx, "origins", originUpdates)

	if requestedOutpoint.OrdinalString() != trueOrigin.OrdinalString() {
		tempSpendsKey := fmt.Sprintf("seq:%s", requestedOutpoint.OrdinalString())
		pipe.Del(ctx, tempSpendsKey)
	}

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to execute migration pipeline: %w", err)
	}

	slog.Debug("Migration complete", "trueOrigin", trueOrigin.OrdinalString())
	return nil
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
	Origin      *transaction.Outpoint
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

func (t *Tracker) Resolve(ctx context.Context, requestedOutpoint *transaction.Outpoint, seq int, includeMap bool) (*ContentResponse, error) {
	slog.Debug("Resolve started",
		"requestedOutpoint", requestedOutpoint.OrdinalString(),
		"seq", seq,
		"includeMap", includeMap)

	knownOriginStr := t.cache.HGet(ctx, "origins", requestedOutpoint.OrdinalString()).Val()
	var origin *transaction.Outpoint

	if knownOriginStr != "" {
		var err error
		origin, err = transaction.OutpointFromString(knownOriginStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse known origin: %w", err)
		}
		slog.Debug("Found known origin", "origin", origin.OrdinalString())
	} else {
		trueOrigin, chain, err := t.backwardCrawl(ctx, requestedOutpoint)
		if err != nil {
			return nil, fmt.Errorf("backward crawl failed: %w", err)
		}

		if err := t.migrateToTrueOrigin(ctx, requestedOutpoint, trueOrigin, chain); err != nil {
			return nil, fmt.Errorf("migration failed: %w", err)
		}

		origin = trueOrigin
	}

	requestedSeqScore := t.cache.ZScore(ctx, fmt.Sprintf("seq:%s", origin.OrdinalString()), requestedOutpoint.OrdinalString()).Val()
	requestedAbsoluteSeq := int(requestedSeqScore)

	targetAbsoluteSeq := requestedAbsoluteSeq + seq
	if seq == -1 {
		targetAbsoluteSeq = -1
	}

	slog.Debug("Resolved origin and calculated target",
		"origin", origin.OrdinalString(),
		"requestedAbsoluteSeq", requestedAbsoluteSeq,
		"relativeSeq", seq,
		"targetAbsoluteSeq", targetAbsoluteSeq)

	spendsKey := fmt.Sprintf("seq:%s", origin.OrdinalString())
	versionsKey := fmt.Sprintf("rev:%s", origin.OrdinalString())
	mapKey := fmt.Sprintf("map:%s", origin.OrdinalString())

	lastRevision := &ContentResponse{
		Origin:    origin,
		MergedMap: make(map[string]string),
	}

	if targetAbsoluteSeq >= 0 {
		seqMembers := t.cache.ZRangeByScore(ctx, spendsKey, &redis.ZRangeBy{
			Min:   fmt.Sprintf("%d", targetAbsoluteSeq),
			Max:   fmt.Sprintf("%d", targetAbsoluteSeq),
			Count: 1,
		}).Val()
		if len(seqMembers) > 0 {
			lastRevision.Outpoint, _ = transaction.OutpointFromString(seqMembers[0])
			lastRevision.Sequence = targetAbsoluteSeq
			slog.Debug("Found cached sequence", "absoluteSeq", targetAbsoluteSeq, "outpoint", lastRevision.Outpoint.OrdinalString())
		}
	}

	var crawlStartSeq int = -1
	var crawlStartOutpoint *transaction.Outpoint

	if targetAbsoluteSeq == -1 || lastRevision.Outpoint == nil {
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
			if spendTxid != nil {
				currentTx, err = t.txLoader.LoadTx(ctx, spendTxid.String())
				if err != nil {
					return nil, fmt.Errorf("failed to load spending tx: %w", err)
				}

				lastRevision.Outpoint, err = t.calculateOrdinalOutput(ctx, currentTx, crawlStartOutpoint)
				if err != nil {
					return nil, fmt.Errorf("failed to calculate ordinal output: %w", err)
				}
				lastRevision.Sequence = crawlStartSeq + 1
			} else {
				// End of chain - the last cached position is our final position
				lastRevision.Outpoint = crawlStartOutpoint
				lastRevision.Sequence = crawlStartSeq
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
		for currentTx != nil && lastRevision.Outpoint != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			// Parse current transaction at outpoint
			slog.Debug("Crawling", "seq", lastRevision.Sequence, "outpoint", lastRevision.Outpoint.OrdinalString())

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
			if targetAbsoluteSeq >= 0 && lastRevision.Sequence >= targetAbsoluteSeq {
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
				if targetAbsoluteSeq >= 0 && mergedSeq >= targetAbsoluteSeq {
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
		if targetAbsoluteSeq == -1 && lastRevision.Sequence >= 0 {
			targetAbsoluteSeq = lastRevision.Sequence
		}
	}

	// Check if we reached the target sequence
	if targetAbsoluteSeq >= 0 && lastRevision.Sequence < targetAbsoluteSeq {
		return nil, fmt.Errorf("sequence %d not found (chain ends at %d): %w", targetAbsoluteSeq, lastRevision.Sequence, txloader.ErrNotFound)
	}

	// Build response
	// If we crawled and found content, lastRevision already has it
	if lastRevision.Content == nil {
		// Load last revision from cache
		revMembers := t.cache.ZRevRangeByScoreWithScores(ctx, versionsKey, &redis.ZRangeBy{
			Min: "0",
			Max: fmt.Sprintf("%d", targetAbsoluteSeq),
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
		// Load all MAP entries up to target sequence
		allMaps := t.loadMergedMap(ctx, origin, targetAbsoluteSeq)

		// Merge with what we accumulated during crawl
		if len(allMaps) > 0 {
			if lastRevision.MergedMap == nil {
				lastRevision.MergedMap = make(map[string]string)
			}
			// Apply maps in sequence order
			for k, v := range allMaps {
				lastRevision.MergedMap[k] = v
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
