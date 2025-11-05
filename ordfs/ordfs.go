package ordfs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/go-ordfs-server/loader"
)

const (
	lockTTL             = 15 * time.Second
	lockRefreshInterval = 5 * time.Second
	lockCheckInterval   = 10 * time.Second
	resolveTimeout      = 60 * time.Second
	cacheTTL            = 30 * 24 * time.Hour
)

type Ordfs struct {
	loader loader.Loader
	cache  *redis.Client
}

func New(ldr loader.Loader, cache *redis.Client) *Ordfs {
	return &Ordfs{
		loader: ldr,
		cache:  cache,
	}
}

func (o *Ordfs) Load(ctx context.Context, req *Request) (*Response, error) {
	if req.Txid != nil {
		return o.loadByTxid(ctx, req)
	}

	output, err := o.loader.LoadOutput(ctx, req.Outpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to load output: %w", err)
	}

	var loadReq *LoadRequest
	if output.Satoshis != 1 || req.Seq == nil {
		resp := o.parseOutput(ctx, req.Outpoint, output, req.Content)
		resp.Outpoint = req.Outpoint
		return resp, nil
	}

	fullResolution, err := o.Resolve(ctx, req.Outpoint, *req.Seq)
	if err != nil {
		return nil, err
	}

	loadReq = &LoadRequest{
		Outpoint: fullResolution.Current,
		Origin:   fullResolution.Origin,
		Sequence: &fullResolution.Sequence,
	}

	if req.Content {
		loadReq.Content = fullResolution.Content
	}
	if req.Map {
		loadReq.Map = fullResolution.Map
	}
	if req.Output {
		loadReq.Output = fullResolution.Current
	}
	if req.Parent {
		loadReq.Parent = fullResolution.Parent
	}

	response, err := o.LoadResolution(ctx, loadReq)
	if err != nil {
		return nil, err
	}

	if !req.Content {
		response.Content = nil
	}

	return response, nil
}

func (o *Ordfs) loadByTxid(ctx context.Context, req *Request) (*Response, error) {
	tx, err := o.loader.LoadTx(ctx, req.Txid.String())
	if err != nil {
		return nil, fmt.Errorf("transaction not found: %w", err)
	}

	for i, output := range tx.Outputs {
		outpoint := &transaction.Outpoint{
			Txid:  *req.Txid,
			Index: uint32(i),
		}
		resp := o.parseOutput(ctx, outpoint, output, req.Content)
		if resp.Content != nil {
			resp.Outpoint = outpoint
			resp.Sequence = 0

			if !req.Content {
				resp.Content = nil
			}
			if !req.Map {
				resp.Map = ""
			}
			if req.Output {
				resp.Output = output.Bytes()
			}

			return resp, nil
		}
	}

	return nil, fmt.Errorf("no inscription or B protocol content found: %w", loader.ErrNotFound)
}
func (o *Ordfs) lockKey(outpoint *transaction.Outpoint) string {
	return fmt.Sprintf("lock:%s", outpoint.OrdinalString())
}

func (o *Ordfs) channelKey(outpoint *transaction.Outpoint) string {
	return fmt.Sprintf("channel:%s", outpoint.OrdinalString())
}

func (o *Ordfs) setLock(ctx context.Context, outpoint *transaction.Outpoint) error {
	return o.cache.SetNX(ctx, o.lockKey(outpoint), "1", lockTTL).Err()
}

func (o *Ordfs) releaseLock(outpoint *transaction.Outpoint) {
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := o.cache.Del(timeoutCtx, o.lockKey(outpoint)).Err(); err != nil {
		slog.Debug("Failed to release lock", "outpoint", outpoint.OrdinalString(), "error", err)
	}
}

func (o *Ordfs) publishCrawlComplete(outpoints []*transaction.Outpoint, origin string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, outpoint := range outpoints {
		if err := o.cache.Publish(ctx, o.channelKey(outpoint), origin).Err(); err != nil {
			slog.Debug("Failed to publish completion", "outpoint", outpoint.OrdinalString(), "error", err)
		}
	}
}

func (o *Ordfs) publishCrawlFailure(outpoints []*transaction.Outpoint) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, outpoint := range outpoints {
		if err := o.cache.Publish(ctx, o.channelKey(outpoint), "").Err(); err != nil {
			slog.Debug("Failed to publish failure", "outpoint", outpoint.OrdinalString(), "error", err)
		}
	}
}

func (o *Ordfs) waitForCrawl(ctx context.Context, outpoint *transaction.Outpoint) error {
	pubsub := o.cache.Subscribe(ctx, o.channelKey(outpoint))
	defer pubsub.Close()

	ch := pubsub.Channel()
	ticker := time.NewTicker(lockCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg := <-ch:
			if msg.Payload != "" {
				slog.Debug("Received pub/sub message", "outpoint", outpoint.OrdinalString(), "origin", msg.Payload)
				return nil
			}
			slog.Debug("Received failure message from pub/sub", "outpoint", outpoint.OrdinalString())
			return fmt.Errorf("other crawl failed")
		case <-ticker.C:
			exists, err := o.cache.Exists(ctx, o.lockKey(outpoint)).Result()
			if err != nil {
				return fmt.Errorf("failed to check lock: %w", err)
			}
			if exists == 0 {
				slog.Debug("Lock cleared during wait", "outpoint", outpoint.OrdinalString())
				return nil
			}
		}
	}
}

func (o *Ordfs) calculateOrdinalOutput(ctx context.Context, spendTx *transaction.Transaction, spentOutpoint *transaction.Outpoint) (*transaction.Outpoint, error) {
	var inputIndex int = -1
	var ordinalOffset uint64 = 0

	for i, input := range spendTx.Inputs {
		if input.SourceTXID != nil && input.SourceTXID.Equal(spentOutpoint.Txid) && input.SourceTxOutIndex == spentOutpoint.Index {
			inputIndex = i
			break
		}

		prevOutpoint := &transaction.Outpoint{
			Txid:  *input.SourceTXID,
			Index: input.SourceTxOutIndex,
		}

		prevOutput, err := o.loader.LoadOutput(ctx, prevOutpoint)
		if err != nil {
			return nil, fmt.Errorf("failed to load input output %s: %w", prevOutpoint.OrdinalString(), err)
		}

		ordinalOffset += prevOutput.Satoshis
	}

	if inputIndex == -1 {
		return nil, fmt.Errorf("outpoint not found in spending transaction inputs")
	}

	var cumulativeSats uint64 = 0
	for i, output := range spendTx.Outputs {
		if output.Satoshis == 0 {
			continue
		}

		if cumulativeSats == ordinalOffset {
			if output.Satoshis != 1 {
				return nil, nil
			}
			return &transaction.Outpoint{
				Txid:  *spendTx.TxID(),
				Index: uint32(i),
			}, nil
		}

		cumulativeSats += output.Satoshis
		if cumulativeSats > ordinalOffset {
			break
		}
	}

	return nil, fmt.Errorf("ordinal output not found (no 1-sat output at ordinal offset)")
}

func (o *Ordfs) calculatePreviousOrdinalInput(ctx context.Context, tx *transaction.Transaction, currentOutpoint *transaction.Outpoint) (*transaction.Outpoint, error) {
	if int(currentOutpoint.Index) >= len(tx.Outputs) {
		return nil, fmt.Errorf("invalid outpoint index")
	}

	currentOutput := tx.Outputs[currentOutpoint.Index]
	if currentOutput.Satoshis != 1 {
		return nil, fmt.Errorf("output is not a 1-sat output")
	}

	var ordinalOffset uint64 = 0
	for i := 0; i < int(currentOutpoint.Index); i++ {
		if tx.Outputs[i].Satoshis > 0 {
			ordinalOffset += tx.Outputs[i].Satoshis
		}
	}

	slog.Debug("calculatePreviousOrdinalInput",
		"outpoint", currentOutpoint.OrdinalString(),
		"txid", currentOutpoint.Txid.String(),
		"outputIndex", currentOutpoint.Index,
		"numOutputs", len(tx.Outputs),
		"numInputs", len(tx.Inputs),
		"ordinalOffset", ordinalOffset)

	var cumulativeSats uint64 = 0
	for i, input := range tx.Inputs {
		prevOutpoint := &transaction.Outpoint{
			Txid:  *input.SourceTXID,
			Index: input.SourceTxOutIndex,
		}

		prevOutput, err := o.loader.LoadOutput(ctx, prevOutpoint)
		if err != nil {
			return nil, fmt.Errorf("failed to load input output %s: %w", prevOutpoint.OrdinalString(), err)
		}

		slog.Debug("Checking input",
			"inputIndex", i,
			"prevOutpoint", prevOutpoint.OrdinalString(),
			"prevSatoshis", prevOutput.Satoshis,
			"cumulativeSats", cumulativeSats,
			"ordinalOffset", ordinalOffset)

		if cumulativeSats == ordinalOffset {
			if prevOutput.Satoshis != 1 {
				slog.Debug("Found offset but not 1-sat", "satoshis", prevOutput.Satoshis)
				return nil, nil
			}
			slog.Debug("Found previous ordinal input", "prevOutpoint", prevOutpoint.OrdinalString())
			return prevOutpoint, nil
		}

		cumulativeSats += prevOutput.Satoshis
		if cumulativeSats > ordinalOffset {
			slog.Debug("Ordinal offset found within multi-sat input, this is the origin")
			return nil, nil
		}
	}

	slog.Debug("Finished checking all inputs without exact match, this is the origin")
	return nil, nil
}

func (o *Ordfs) saveBackwardProgress(ctx context.Context, requestedOutpoint *transaction.Outpoint, chain []ChainEntry) {
	seqKey := fmt.Sprintf("seq:%s", requestedOutpoint.OrdinalString())

	members := make([]redis.Z, len(chain))
	for i, entry := range chain {
		members[i] = redis.Z{
			Score:  float64(entry.RelativeSeq),
			Member: entry.Outpoint.OrdinalString(),
		}
	}

	o.cache.ZAdd(ctx, seqKey, members...)
	slog.Debug("Saved backward progress", "key", seqKey, "count", len(chain))
}

func (o *Ordfs) backwardCrawl(ctx context.Context, requestedOutpoint *transaction.Outpoint) (*transaction.Outpoint, error) {
	lockedOutpoints := []*transaction.Outpoint{}
	defer func() {
		for _, outpoint := range lockedOutpoints {
			o.releaseLock(outpoint)
		}
	}()

	crawlCtx, cancelCrawl := context.WithCancel(ctx)
	defer cancelCrawl()

	go func() {
		ticker := time.NewTicker(lockRefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-crawlCtx.Done():
				return
			case <-ticker.C:
				for _, outpoint := range lockedOutpoints {
					if err := o.cache.Set(crawlCtx, o.lockKey(outpoint), "1", lockTTL).Err(); err != nil {
						slog.Debug("Failed to refresh lock", "outpoint", outpoint.OrdinalString(), "error", err)
					}
				}
			}
		}
	}()

	currentOutpoint := requestedOutpoint
	relativeSeq := 0
	var chain []ChainEntry

	slog.Debug("Starting backward crawl", "outpoint", requestedOutpoint.OrdinalString())

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		knownOrigin := o.cache.HGet(ctx, "origins", currentOutpoint.OrdinalString()).Val()
		if knownOrigin != "" {
			slog.Debug("Found known origin", "outpoint", currentOutpoint.OrdinalString(), "origin", knownOrigin)
			origin, err := transaction.OutpointFromString(knownOrigin)
			if err != nil {
				return nil, fmt.Errorf("failed to parse known origin: %w", err)
			}
			if err := o.migrateToOrigin(ctx, requestedOutpoint, origin, chain); err != nil {
				o.publishCrawlFailure(lockedOutpoints)
				return nil, fmt.Errorf("migration failed: %w", err)
			}
			o.publishCrawlComplete(lockedOutpoints, origin.OrdinalString())
			return origin, nil
		}

		if err := o.setLock(ctx, currentOutpoint); err != nil {
			slog.Debug("Lock already held, waiting for crawl to complete", "outpoint", currentOutpoint.OrdinalString())
			if err := o.waitForCrawl(ctx, currentOutpoint); err != nil {
				return nil, err
			}

			knownOrigin = o.cache.HGet(ctx, "origins", currentOutpoint.OrdinalString()).Val()
			if knownOrigin != "" {
				origin, err := transaction.OutpointFromString(knownOrigin)
				if err != nil {
					return nil, fmt.Errorf("failed to parse origin after wait: %w", err)
				}
				slog.Debug("Using origin from completed crawl", "origin", origin.OrdinalString())
				if err := o.migrateToOrigin(ctx, requestedOutpoint, origin, chain); err != nil {
					o.publishCrawlFailure(lockedOutpoints)
					return nil, fmt.Errorf("migration failed: %w", err)
				}
				o.publishCrawlComplete(lockedOutpoints, origin.OrdinalString())
				return origin, nil
			}

			slog.Debug("No origin found after wait, attempting crawl", "outpoint", currentOutpoint.OrdinalString())
			if err := o.setLock(ctx, currentOutpoint); err != nil {
				return nil, fmt.Errorf("failed to acquire lock after wait: %w", err)
			}
		}

		lockedOutpoints = append(lockedOutpoints, currentOutpoint)

		currentTx, err := o.loader.LoadTx(ctx, currentOutpoint.Txid.String())
		if err != nil {
			return nil, fmt.Errorf("failed to load tx %s: %w", currentOutpoint.Txid.String(), err)
		}

		if int(currentOutpoint.Index) >= len(currentTx.Outputs) {
			return nil, fmt.Errorf("invalid outpoint index")
		}

		currentOutput := currentTx.Outputs[currentOutpoint.Index]
		resp := o.parseOutput(ctx, currentOutpoint, currentOutput, true)

		var entryContentOutpoint, entryMapOutpoint, entryParentOutpoint *transaction.Outpoint
		if resp.Content != nil {
			entryContentOutpoint = currentOutpoint
		}
		if resp.Map != "" {
			entryMapOutpoint = currentOutpoint
		}
		if resp.Parent != nil {
			entryParentOutpoint = currentOutpoint
		}

		chain = append(chain, ChainEntry{
			Outpoint:        currentOutpoint,
			RelativeSeq:     relativeSeq,
			ContentOutpoint: entryContentOutpoint,
			MapOutpoint:     entryMapOutpoint,
			ParentOutpoint:  entryParentOutpoint,
		})

		prevOutpoint, err := o.calculatePreviousOrdinalInput(ctx, currentTx, currentOutpoint)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate previous input: %w", err)
		}

		if prevOutpoint == nil {
			slog.Debug("Found origin", "outpoint", currentOutpoint.OrdinalString(), "depth", -relativeSeq)
			if err := o.migrateToOrigin(ctx, requestedOutpoint, currentOutpoint, chain); err != nil {
				o.publishCrawlFailure(lockedOutpoints)
				return nil, fmt.Errorf("migration failed: %w", err)
			}
			o.publishCrawlComplete(lockedOutpoints, currentOutpoint.OrdinalString())
			return currentOutpoint, nil
		}

		if relativeSeq%10 == 0 {
			o.saveBackwardProgress(ctx, requestedOutpoint, chain)
		}

		slog.Debug("Backward crawl step", "outpoint", currentOutpoint.OrdinalString(), "relativeSeq", relativeSeq)

		relativeSeq--
		currentOutpoint = prevOutpoint
	}
}

func (o *Ordfs) migrateToOrigin(ctx context.Context, requestedOutpoint, origin *transaction.Outpoint, chain []ChainEntry) error {
	var offset int
	if len(chain) > 0 {
		offset = -chain[len(chain)-1].RelativeSeq
	}

	slog.Debug("Migrating to origin",
		"requestedOutpoint", requestedOutpoint.OrdinalString(),
		"origin", origin.OrdinalString(),
		"offset", offset,
		"chainLength", len(chain))

	pipe := o.cache.Pipeline()

	originSeqKey := fmt.Sprintf("seq:%s", origin.OrdinalString())
	originRevKey := fmt.Sprintf("rev:%s", origin.OrdinalString())
	originMapKey := fmt.Sprintf("map:%s", origin.OrdinalString())
	originParentKey := fmt.Sprintf("parents:%s", origin.OrdinalString())

	members := make([]redis.Z, len(chain))
	originUpdates := make(map[string]interface{})

	for i, entry := range chain {
		absoluteSeq := entry.RelativeSeq + offset
		members[i] = redis.Z{
			Score:  float64(absoluteSeq),
			Member: entry.Outpoint.OrdinalString(),
		}
		originUpdates[entry.Outpoint.OrdinalString()] = origin.OrdinalString()

		if entry.ContentOutpoint != nil {
			pipe.ZAdd(ctx, originRevKey, redis.Z{
				Score:  float64(absoluteSeq),
				Member: entry.ContentOutpoint.OrdinalString(),
			})
		}

		if entry.MapOutpoint != nil {
			pipe.ZAdd(ctx, originMapKey, redis.Z{
				Score:  float64(absoluteSeq),
				Member: entry.MapOutpoint.OrdinalString(),
			})
		}

		if entry.ParentOutpoint != nil {
			pipe.ZAdd(ctx, originParentKey, redis.Z{
				Score:  float64(absoluteSeq),
				Member: entry.ParentOutpoint.OrdinalString(),
			})
		}
	}

	pipe.ZAdd(ctx, originSeqKey, members...)
	pipe.HSet(ctx, "origins", originUpdates)

	if requestedOutpoint.OrdinalString() != origin.OrdinalString() {
		tempSeqKey := fmt.Sprintf("seq:%s", requestedOutpoint.OrdinalString())
		pipe.Del(ctx, tempSeqKey)
	}

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to execute migration pipeline: %w", err)
	}

	slog.Debug("Migration complete", "origin", origin.OrdinalString())
	return nil
}

func (o *Ordfs) forwardCrawl(ctx context.Context, req *ForwardCrawlRequest) (*ForwardCrawlResponse, error) {
	seqKey := fmt.Sprintf("seq:%s", req.Origin.OrdinalString())
	revKey := fmt.Sprintf("rev:%s", req.Origin.OrdinalString())
	mapKey := fmt.Sprintf("map:%s", req.Origin.OrdinalString())
	parentKey := fmt.Sprintf("parents:%s", req.Origin.OrdinalString())

	currentOutpoint := req.StartOutpoint
	currentSeq := req.StartSeq
	parentFound := false

	slog.Debug("Starting forward crawl", "origin", req.Origin.OrdinalString(), "start", req.StartOutpoint.OrdinalString(), "startSeq", req.StartSeq, "targetSeq", req.TargetSeq, "parentValidation", req.ParentOutpoints != nil)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		o.cache.HSet(ctx, "origins", currentOutpoint.OrdinalString(), req.Origin.OrdinalString())

		if req.ParentOutpoints != nil && req.ParentOutpoints[currentOutpoint.OrdinalString()] {
			slog.Debug("Found parent match during crawl", "outpoint", currentOutpoint.OrdinalString())
			return &ForwardCrawlResponse{
				Outpoint:    currentOutpoint,
				Sequence:    currentSeq,
				ParentFound: true,
			}, nil
		}

		tx, err := o.loader.LoadTx(ctx, currentOutpoint.Txid.String())
		if err != nil {
			return nil, fmt.Errorf("failed to load tx: %w", err)
		}

		if int(currentOutpoint.Index) >= len(tx.Outputs) {
			return nil, fmt.Errorf("invalid outpoint index")
		}

		output := tx.Outputs[currentOutpoint.Index]
		resp := o.parseOutput(ctx, currentOutpoint, output, true)

		o.cache.ZAdd(ctx, seqKey, redis.Z{
			Score:  float64(currentSeq),
			Member: currentOutpoint.OrdinalString(),
		})

		if resp.Content != nil {
			o.cache.ZAdd(ctx, revKey, redis.Z{
				Score:  float64(currentSeq),
				Member: currentOutpoint.OrdinalString(),
			})
		}

		if resp.Map != "" {
			o.cache.ZAdd(ctx, mapKey, redis.Z{
				Score:  float64(currentSeq),
				Member: currentOutpoint.OrdinalString(),
			})
		}

		if resp.Parent != nil {
			o.cache.ZAdd(ctx, parentKey, redis.Z{
				Score:  float64(currentSeq),
				Member: currentOutpoint.OrdinalString(),
			})
		}

		slog.Debug("Forward crawl step", "seq", currentSeq, "outpoint", currentOutpoint.OrdinalString(), "hasContent", resp.Content != nil, "hasMap", resp.Map != "")

		if req.TargetSeq >= 0 && currentSeq >= req.TargetSeq {
			slog.Debug("Reached target sequence", "seq", currentSeq, "target", req.TargetSeq)
			break
		}

		nextSpendKey := fmt.Sprintf("seq:%s", currentOutpoint.OrdinalString())
		if o.cache.ZCard(ctx, nextSpendKey).Val() > 1 {
			mergedOutpoint, mergedSeq, err := o.mergeExistingChain(ctx, req.Origin, currentOutpoint, currentSeq)
			if err != nil {
				return nil, fmt.Errorf("failed to merge existing chain: %w", err)
			}

			slog.Debug("Merged with existing chain", "from", currentSeq, "to", mergedSeq)

			if req.TargetSeq >= 0 && mergedSeq >= req.TargetSeq {
				targetMembers := o.cache.ZRangeByScore(ctx, seqKey, &redis.ZRangeBy{
					Min:   fmt.Sprintf("%d", req.TargetSeq),
					Max:   fmt.Sprintf("%d", req.TargetSeq),
					Count: 1,
				}).Val()
				if len(targetMembers) > 0 {
					if targetOutpoint, err := transaction.OutpointFromString(targetMembers[0]); err == nil {
						return &ForwardCrawlResponse{
							Outpoint:    targetOutpoint,
							Sequence:    req.TargetSeq,
							ParentFound: parentFound,
						}, nil
					}
				}
				return &ForwardCrawlResponse{
					Outpoint:    mergedOutpoint,
					Sequence:    mergedSeq,
					ParentFound: parentFound,
				}, nil
			}

			currentOutpoint = mergedOutpoint
			currentSeq = mergedSeq
		}

		spendTxid, err := o.loader.GetSpend(ctx, currentOutpoint.OrdinalString())
		if err != nil {
			return nil, fmt.Errorf("failed to get spend: %w", err)
		}
		if spendTxid == nil {
			slog.Debug("Reached end of chain", "seq", currentSeq)
			break
		}

		spendTx, err := o.loader.LoadTx(ctx, spendTxid.String())
		if err != nil {
			return nil, fmt.Errorf("failed to load spending tx: %w", err)
		}

		nextOutpoint, err := o.calculateOrdinalOutput(ctx, spendTx, currentOutpoint)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate ordinal output: %w", err)
		}

		currentOutpoint = nextOutpoint
		currentSeq++
	}

	slog.Debug("Forward crawl complete", "finalSeq", currentSeq, "finalOutpoint", currentOutpoint.OrdinalString())
	return &ForwardCrawlResponse{
		Outpoint:    currentOutpoint,
		Sequence:    currentSeq,
		ParentFound: parentFound,
	}, nil
}

func (o *Ordfs) loadMergedMap(ctx context.Context, origin *transaction.Outpoint, mapOutpoint *transaction.Outpoint) (map[string]string, error) {
	mergedKey := fmt.Sprintf("merged:%s", mapOutpoint.OrdinalString())

	cached := o.cache.Get(ctx, mergedKey).Val()
	if cached != "" {
		var mergedMap map[string]string
		if err := json.Unmarshal([]byte(cached), &mergedMap); err == nil {
			return mergedMap, nil
		}
	}

	mapKey := fmt.Sprintf("map:%s", origin.OrdinalString())
	mapScore := o.cache.ZScore(ctx, mapKey, mapOutpoint.OrdinalString()).Val()

	mapOutpoints := o.cache.ZRangeByScore(ctx, mapKey, &redis.ZRangeBy{
		Min: "0",
		Max: fmt.Sprintf("%f", mapScore),
	}).Val()

	mergedMap := make(map[string]string)
	for _, outpointStr := range mapOutpoints {
		outpoint, err := transaction.OutpointFromString(outpointStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse outpoint %s: %w", outpointStr, err)
		}

		cacheKey := fmt.Sprintf("parsed:%s", outpoint.OrdinalString())
		var individualMap map[string]string

		mapJSON := o.cache.HGet(ctx, cacheKey, "map").Val()
		if mapJSON != "" {
			if err := json.Unmarshal([]byte(mapJSON), &individualMap); err != nil {
				return nil, fmt.Errorf("failed to unmarshal cached map for %s: %w", outpoint.OrdinalString(), err)
			}
		} else if o.cache.Exists(ctx, cacheKey).Val() == 0 {
			resp, err := o.loadAndParse(ctx, outpoint, true)
			if err != nil {
				return nil, fmt.Errorf("failed to load and parse outpoint %s: %w", outpoint.OrdinalString(), err)
			}
			if resp.Map != "" {
				if err := json.Unmarshal([]byte(resp.Map), &individualMap); err != nil {
					return nil, fmt.Errorf("failed to unmarshal response map for %s: %w", outpoint.OrdinalString(), err)
				}
			}
		}

		for k, v := range individualMap {
			mergedMap[k] = v
		}
	}

	if mergedJSON, err := json.Marshal(mergedMap); err == nil {
		o.cache.Set(ctx, mergedKey, string(mergedJSON), cacheTTL)
	}

	return mergedMap, nil
}

func (o *Ordfs) validateParent(ctx context.Context, parentOutpoint *transaction.Outpoint, childOrigin *transaction.Outpoint) (*transaction.Outpoint, error) {
	parentOriginStr := o.cache.HGet(ctx, "origins", parentOutpoint.OrdinalString()).Val()
	var parentOrigin *transaction.Outpoint

	if parentOriginStr == "" {
		// Parent hasn't been crawled yet - crawl it now to find its origin
		slog.Debug("Parent origin not found, crawling", "parentOutpoint", parentOutpoint.OrdinalString())
		var err error
		parentOrigin, err = o.backwardCrawl(ctx, parentOutpoint)
		if err != nil {
			return nil, fmt.Errorf("failed to crawl parent: %w", err)
		}
	} else {
		var err error
		parentOrigin, err = transaction.OutpointFromString(parentOriginStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse parent origin: %w", err)
		}
	}

	childOriginTx, err := o.loader.LoadTx(ctx, childOrigin.Txid.String())
	if err != nil {
		return nil, fmt.Errorf("failed to load child origin tx: %w", err)
	}

	for _, input := range childOriginTx.Inputs {
		if input.SourceTXID != nil {
			inputOutpoint := &transaction.Outpoint{
				Txid:  *input.SourceTXID,
				Index: input.SourceTxOutIndex,
			}

			inputOriginStr := o.cache.HGet(ctx, "origins", inputOutpoint.OrdinalString()).Val()
			if inputOriginStr == parentOrigin.OrdinalString() {
				slog.Debug("Parent validated via cached input origin", "input", inputOutpoint.OrdinalString(), "parentOrigin", parentOrigin.OrdinalString())
				return parentOrigin, nil
			}
		}
	}

	inputOutpoints := make(map[string]bool)
	for _, input := range childOriginTx.Inputs {
		if input.SourceTXID != nil {
			outpoint := &transaction.Outpoint{
				Txid:  *input.SourceTXID,
				Index: input.SourceTxOutIndex,
			}
			inputOutpoints[outpoint.OrdinalString()] = true
		}
	}

	crawlResp, err := o.forwardCrawl(ctx, &ForwardCrawlRequest{
		Origin:          parentOrigin,
		StartOutpoint:   parentOrigin,
		StartSeq:        0,
		TargetSeq:       -1,
		ParentOutpoints: inputOutpoints,
	})

	if err != nil {
		return nil, fmt.Errorf("parent validation crawl failed: %w", err)
	}

	if !crawlResp.ParentFound {
		return nil, fmt.Errorf("parent origin was not spent in child origin transaction")
	}

	return parentOrigin, nil
}

func (o *Ordfs) LoadResolution(ctx context.Context, req *LoadRequest) (response *Response, err error) {
	response = &Response{
		Outpoint: req.Outpoint,
		Origin:   req.Origin,
	}
	if req.Sequence != nil {
		response.Sequence = *req.Sequence
	}

	contentCache := make(map[transaction.Outpoint]*Response)

	if req.Output != nil {
		output, err := o.loader.LoadOutput(ctx, req.Output)
		if err != nil {
			return nil, fmt.Errorf("failed to load output: %w", err)
		}
		response.Output = output.Bytes()
	}

	if req.Content != nil {
		resp, err := o.loadAndParse(ctx, req.Content, true)
		if err != nil {
			return nil, fmt.Errorf("failed to load content: %w", err)
		}
		contentCache[*req.Content] = resp
		response.ContentType = resp.ContentType
		response.Content = resp.Content
		response.ContentLength = resp.ContentLength
	}

	if req.Map != nil {
		if req.Sequence == nil {
			var cachedResponse *Response
			if cachedResponse = contentCache[*req.Content]; cachedResponse == nil {
				cachedResponse, err = o.loadAndParse(ctx, req.Content, true)
				if err != nil {
					return nil, fmt.Errorf("failed to load content: %w", err)
				}
				contentCache[*req.Content] = cachedResponse
			}
			response.Map = cachedResponse.Map
		} else {
			mergedMap, err := o.loadMergedMap(ctx, req.Origin, req.Map)
			if err != nil {
				return nil, fmt.Errorf("failed to load merged map: %w", err)
			}
			if mergedJSON, err := json.Marshal(mergedMap); err == nil {
				response.Map = string(mergedJSON)
			}
		}
	}

	if req.Parent != nil {
		var cachedResponse *Response
		if cachedResponse = contentCache[*req.Parent]; cachedResponse == nil {
			cachedResponse, err = o.loadAndParse(ctx, req.Parent, false)
			if err != nil {
				return nil, fmt.Errorf("failed to load parent outpoint: %w", err)
			}
			contentCache[*req.Parent] = cachedResponse
		}
		if cachedResponse.Parent != nil {
			parentOrigin, err := o.validateParent(ctx, cachedResponse.Parent, req.Origin)
			if err != nil {
				return nil, fmt.Errorf("parent validation failed: %w", err)
			}
			response.Parent = parentOrigin
		}
	}

	return response, nil
}

func (o *Ordfs) Resolve(ctx context.Context, requestedOutpoint *transaction.Outpoint, seq int) (*Resolution, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	slog.Debug("Resolve started",
		"requestedOutpoint", requestedOutpoint.OrdinalString(),
		"seq", seq)

	// if seq == nil {
	// 	return &Resolution{
	// 		Origin:   requestedOutpoint,
	// 		Current:  requestedOutpoint,
	// 		Content:  requestedOutpoint,
	// 		Map:      requestedOutpoint,
	// 		Sequence: nil,
	// 	}, nil
	// }

	knownOriginStr := o.cache.HGet(ctx, "origins", requestedOutpoint.OrdinalString()).Val()
	var origin *transaction.Outpoint

	if knownOriginStr != "" {
		var err error
		origin, err = transaction.OutpointFromString(knownOriginStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse known origin: %w", err)
		}
		slog.Debug("Found known origin", "origin", origin.OrdinalString())
	} else {
		var err error
		origin, err = o.backwardCrawl(ctx, requestedOutpoint)
		if err != nil {
			return nil, fmt.Errorf("backward crawl failed: %w", err)
		}
	}

	seqKey := fmt.Sprintf("seq:%s", origin.OrdinalString())
	requestedSeqScore := o.cache.ZScore(ctx, seqKey, requestedOutpoint.OrdinalString()).Val()
	requestedAbsoluteSeq := int(requestedSeqScore)

	targetAbsoluteSeq := seq
	if seq == -1 {
		targetAbsoluteSeq = -1
	}

	slog.Debug("Resolved origin and calculated target",
		"origin", origin.OrdinalString(),
		"requestedAbsoluteSeq", requestedAbsoluteSeq,
		"relativeSeq", seq,
		"targetAbsoluteSeq", targetAbsoluteSeq)

	revKey := fmt.Sprintf("rev:%s", origin.OrdinalString())
	mapKey := fmt.Sprintf("map:%s", origin.OrdinalString())

	var targetOutpoint *transaction.Outpoint
	if targetAbsoluteSeq >= 0 {
		seqMembers := o.cache.ZRangeByScore(ctx, seqKey, &redis.ZRangeBy{
			Min:   fmt.Sprintf("%d", targetAbsoluteSeq),
			Max:   fmt.Sprintf("%d", targetAbsoluteSeq),
			Count: 1,
		}).Val()
		if len(seqMembers) > 0 {
			var err error
			targetOutpoint, err = transaction.OutpointFromString(seqMembers[0])
			if err != nil {
				return nil, fmt.Errorf("failed to parse cached target outpoint: %w", err)
			}
			slog.Debug("Found cached target sequence", "absoluteSeq", targetAbsoluteSeq, "outpoint", targetOutpoint.OrdinalString())
		}
	}

	if targetOutpoint == nil {
		var crawlStartOutpoint *transaction.Outpoint
		var crawlStartSeq int

		lastSpendMembers := o.cache.ZRevRangeWithScores(ctx, seqKey, 0, 0).Val()
		if len(lastSpendMembers) > 0 {
			crawlStartSeq = int(lastSpendMembers[0].Score)
			var err error
			crawlStartOutpoint, err = transaction.OutpointFromString(lastSpendMembers[0].Member.(string))
			if err != nil {
				return nil, fmt.Errorf("failed to parse cached crawl start outpoint: %w", err)
			}
			slog.Debug("Resuming crawl from cached position", "seq", crawlStartSeq, "outpoint", crawlStartOutpoint.OrdinalString())
		} else {
			crawlStartOutpoint = origin
			crawlStartSeq = 0
			slog.Debug("Starting crawl from origin", "outpoint", origin.OrdinalString())
		}

		crawlResp, err := o.forwardCrawl(ctx, &ForwardCrawlRequest{
			Origin:        origin,
			StartOutpoint: crawlStartOutpoint,
			StartSeq:      crawlStartSeq,
			TargetSeq:     targetAbsoluteSeq,
		})
		if err != nil {
			return nil, fmt.Errorf("forward crawl failed: %w", err)
		}
		finalSeq := crawlResp.Sequence

		if seq == -1 {
			targetAbsoluteSeq = finalSeq
		}

		if targetAbsoluteSeq >= 0 {
			targetMembers := o.cache.ZRangeByScore(ctx, seqKey, &redis.ZRangeBy{
				Min:   fmt.Sprintf("%d", targetAbsoluteSeq),
				Max:   fmt.Sprintf("%d", targetAbsoluteSeq),
				Count: 1,
			}).Val()
			if len(targetMembers) > 0 {
				var err error
				targetOutpoint, err = transaction.OutpointFromString(targetMembers[0])
				if err != nil {
					return nil, fmt.Errorf("failed to parse target outpoint after crawl: %w", err)
				}
			} else {
				return nil, fmt.Errorf("target sequence %d not found (chain ends at %d): %w", targetAbsoluteSeq, finalSeq, loader.ErrNotFound)
			}
		}
	}

	resolution := &Resolution{
		Origin:   origin,
		Current:  targetOutpoint,
		Sequence: targetAbsoluteSeq,
	}

	revMembers := o.cache.ZRevRangeByScore(ctx, revKey, &redis.ZRangeBy{
		Min: "0",
		Max: fmt.Sprintf("%d", targetAbsoluteSeq),
	}).Val()
	if len(revMembers) > 0 {
		contentOutpoint, err := transaction.OutpointFromString(revMembers[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse cached content outpoint: %w", err)
		}
		resolution.Content = contentOutpoint
	}

	mapMembers := o.cache.ZRevRangeByScore(ctx, mapKey, &redis.ZRangeBy{
		Min: "0",
		Max: fmt.Sprintf("%d", targetAbsoluteSeq),
	}).Val()
	if len(mapMembers) > 0 {
		mapOutpoint, err := transaction.OutpointFromString(mapMembers[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse cached map outpoint: %w", err)
		}
		resolution.Map = mapOutpoint
	}

	parentKey := fmt.Sprintf("parents:%s", origin.OrdinalString())
	parentMembers := o.cache.ZRevRangeByScore(ctx, parentKey, &redis.ZRangeBy{
		Min: "0",
		Max: fmt.Sprintf("%d", targetAbsoluteSeq),
	}).Val()
	if len(parentMembers) > 0 {
		parentOutpoint, err := transaction.OutpointFromString(parentMembers[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse cached parent outpoint: %w", err)
		}
		resolution.Parent = parentOutpoint
	}

	if resolution.Content == nil {
		return nil, fmt.Errorf("no inscription found: %w", loader.ErrNotFound)
	}

	slog.Debug("Resolve complete", "origin", origin.OrdinalString(), "current", resolution.Current.OrdinalString(), "seq", resolution.Sequence, "hasContent", resolution.Content != nil, "hasMap", resolution.Map != nil)
	return resolution, nil
}

func (o *Ordfs) mergeExistingChain(ctx context.Context, origin *transaction.Outpoint, intersectionOutpoint *transaction.Outpoint, currentSeq int) (*transaction.Outpoint, int, error) {
	intersectionSeqKey := fmt.Sprintf("seq:%s", intersectionOutpoint.OrdinalString())
	existingSpends := o.cache.ZRangeWithScores(ctx, intersectionSeqKey, 0, -1).Val()

	if len(existingSpends) == 0 {
		return intersectionOutpoint, currentSeq, nil
	}

	originSeqKey := fmt.Sprintf("seq:%s", origin.OrdinalString())
	originRevKey := fmt.Sprintf("rev:%s", origin.OrdinalString())
	originMapKey := fmt.Sprintf("map:%s", origin.OrdinalString())

	for _, member := range existingSpends {
		outpointStr := member.Member.(string)
		originalScore := int(member.Score)
		newScore := currentSeq + originalScore

		o.cache.ZAdd(ctx, originSeqKey, redis.Z{
			Score:  float64(newScore),
			Member: outpointStr,
		})
	}

	intersectionRevKey := fmt.Sprintf("rev:%s", intersectionOutpoint.OrdinalString())
	existingVersions := o.cache.ZRangeWithScores(ctx, intersectionRevKey, 0, -1).Val()

	for _, member := range existingVersions {
		outpointStr := member.Member.(string)
		originalScore := int(member.Score)
		newScore := currentSeq + originalScore + 1

		o.cache.ZAdd(ctx, originRevKey, redis.Z{
			Score:  float64(newScore),
			Member: outpointStr,
		})
	}

	intersectionMapKey := fmt.Sprintf("map:%s", intersectionOutpoint.OrdinalString())
	existingMaps := o.cache.ZRangeWithScores(ctx, intersectionMapKey, 0, -1).Val()

	for _, member := range existingMaps {
		outpointStr := member.Member.(string)
		originalScore := int(member.Score)
		newScore := currentSeq + originalScore

		o.cache.ZAdd(ctx, originMapKey, redis.Z{
			Score:  float64(newScore),
			Member: outpointStr,
		})
	}

	lastOutpointStr := existingSpends[len(existingSpends)-1].Member.(string)
	lastOutpoint, err := transaction.OutpointFromString(lastOutpointStr)
	if err != nil {
		return intersectionOutpoint, currentSeq, err
	}

	newSequence := currentSeq + len(existingSpends)

	return lastOutpoint, newSequence, nil
}
