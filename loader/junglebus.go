package loader

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/util"
	"github.com/redis/go-redis/v9"
)

const (
	cacheTTL = 365 * 24 * time.Hour // 1 year - makes cache volatile for Redis eviction
)

type JungleBusLoader struct {
	cache        *redis.Client
	junglebusURL string
}

func NewJungleBusLoader(cache *redis.Client, junglebusURL string) *JungleBusLoader {
	return &JungleBusLoader{
		cache:        cache,
		junglebusURL: junglebusURL,
	}
}

func (l *JungleBusLoader) txKey(txid string) string {
	return "tx:" + txid
}

func (l *JungleBusLoader) LoadTx(ctx context.Context, txid string) (*transaction.Transaction, error) {
	cacheKey := l.txKey(txid)

	if rawtx, err := l.cache.Get(ctx, cacheKey).Bytes(); err == nil && len(rawtx) > 0 {
		if tx, err := transaction.NewTransactionFromBytes(rawtx); err == nil {
			return tx, nil
		}
		l.cache.Del(ctx, cacheKey)
	}

	rawtx, err := l.loadRemoteRawtx(ctx, txid)
	if err != nil {
		return nil, err
	}

	tx, err := transaction.NewTransactionFromBytes(rawtx)
	if err != nil {
		return nil, fmt.Errorf("malformed transaction: %w", err)
	}

	l.cache.Set(ctx, cacheKey, rawtx, cacheTTL)

	return tx, nil
}

func (l *JungleBusLoader) LoadOutput(ctx context.Context, outpoint *transaction.Outpoint) (*transaction.TransactionOutput, error) {
	cacheKey := fmt.Sprintf("txo:%s", outpoint.OrdinalString())

	if rawOutput, err := l.cache.Get(ctx, cacheKey).Bytes(); err == nil && len(rawOutput) > 0 {
		if output, err := l.parseOutput(rawOutput); err == nil {
			return output, nil
		}
		l.cache.Del(ctx, cacheKey)
	}

	url := fmt.Sprintf("%s/v1/txo/get/%s", l.junglebusURL, outpoint.OrdinalString())

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, ErrNotFound
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("junglebus returned status %d", resp.StatusCode)
	}

	rawOutput, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	output, err := l.parseOutput(rawOutput)
	if err != nil {
		return nil, fmt.Errorf("malformed output: %w", err)
	}

	l.cache.Set(ctx, cacheKey, rawOutput, cacheTTL)

	return output, nil
}

func (l *JungleBusLoader) parseOutput(bytes []byte) (*transaction.TransactionOutput, error) {
	if len(bytes) < 8 {
		return nil, fmt.Errorf("output too short: %d bytes", len(bytes))
	}

	satoshis := binary.LittleEndian.Uint64(bytes[0:8])

	_, varintSize := util.NewVarIntFromBytes(bytes[8:])

	scriptStart := 8 + varintSize
	lockingScript := script.Script(bytes[scriptStart:])

	return &transaction.TransactionOutput{
		Satoshis:      satoshis,
		LockingScript: &lockingScript,
	}, nil
}

func (l *JungleBusLoader) LoadSpend(ctx context.Context, outpoint string) (*chainhash.Hash, error) {
	url := fmt.Sprintf("%s/v1/txo/spend/%s", l.junglebusURL, outpoint)
	slog.Debug("Fetching spend from JungleBus", "url", url)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		slog.Debug("JungleBus spend lookup error", "outpoint", outpoint, "status", resp.StatusCode)
		return nil, fmt.Errorf("junglebus returned status %d", resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if len(b) == 0 {
		return nil, nil
	}

	txHash, err := chainhash.NewHashFromHex(hex.EncodeToString(b))
	if err != nil {
		return nil, fmt.Errorf("invalid txid from junglebus: %w", err)
	}

	return txHash, nil
}

func (l *JungleBusLoader) LoadMerkleProof(ctx context.Context, txid string) ([]byte, error) {
	cacheKey := "proof:" + txid

	if proof, err := l.cache.Get(ctx, cacheKey).Bytes(); err == nil && len(proof) > 0 {
		return proof, nil
	}

	url := fmt.Sprintf("%s/v1/transaction/proof/%s/bin", l.junglebusURL, txid)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		slog.Debug("Merkle proof not found in JungleBus", "txid", txid)
		return nil, ErrNotFound
	}

	if resp.StatusCode != 200 {
		slog.Debug("JungleBus error fetching merkle proof", "txid", txid, "status", resp.StatusCode)
		return nil, fmt.Errorf("junglebus returned status %d", resp.StatusCode)
	}

	proof, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	l.cache.Set(ctx, cacheKey, proof, cacheTTL)

	return proof, nil
}

func (l *JungleBusLoader) LoadBeef(ctx context.Context, txid string) ([]byte, error) {
	cacheKey := "beef:" + txid

	if beef, err := l.cache.Get(ctx, cacheKey).Bytes(); err == nil && len(beef) > 0 {
		return beef, nil
	}

	url := fmt.Sprintf("%s/v1/transaction/beef/%s", l.junglebusURL, txid)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		slog.Debug("BEEF not found in JungleBus", "txid", txid)
		return nil, ErrNotFound
	}

	if resp.StatusCode != 200 {
		slog.Debug("JungleBus error fetching BEEF", "txid", txid, "status", resp.StatusCode)
		return nil, fmt.Errorf("junglebus returned status %d", resp.StatusCode)
	}

	beef, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	l.cache.Set(ctx, cacheKey, beef, cacheTTL)

	return beef, nil
}

func (l *JungleBusLoader) loadRemoteRawtx(ctx context.Context, txid string) ([]byte, error) {
	url := fmt.Sprintf("%s/v1/transaction/get/%s/bin", l.junglebusURL, txid)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		slog.Debug("Transaction not found in JungleBus", "txid", txid)
		return nil, ErrNotFound
	}

	if resp.StatusCode != 200 {
		slog.Debug("JungleBus error", "txid", txid, "status", resp.StatusCode)
		return nil, fmt.Errorf("junglebus returned status %d", resp.StatusCode)
	}

	rawtx, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return rawtx, nil
}

func (l *JungleBusLoader) LoadHeaderByHash(ctx context.Context, hash string) (*BlockHeaderResponse, error) {
	cacheKey := "blockheader:" + hash

	if header, err := l.cache.Get(ctx, cacheKey).Bytes(); err == nil && len(header) > 0 {
		return &BlockHeaderResponse{
			Header: header,
			Hash:   hash,
		}, nil
	}

	url := fmt.Sprintf("%s/v1/block_header/get/%s/bin", l.junglebusURL, hash)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		slog.Debug("Block header not found in JungleBus", "hash", hash)
		return nil, ErrNotFound
	}

	if resp.StatusCode != 200 {
		slog.Debug("JungleBus error fetching block header", "hash", hash, "status", resp.StatusCode)
		return nil, fmt.Errorf("junglebus returned status %d", resp.StatusCode)
	}

	header, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	l.cache.Set(ctx, cacheKey, header, 0)

	heightStr := resp.Header.Get("X-Block-Height")
	if heightStr != "" {
		if height, err := strconv.ParseUint(heightStr, 10, 32); err == nil {
			heightCacheKey := fmt.Sprintf("blockheight:%d", height)
			l.cache.Set(ctx, heightCacheKey, hash, cacheTTL)
		}
	}

	return &BlockHeaderResponse{
		Header: header,
		Hash:   hash,
	}, nil
}

func (l *JungleBusLoader) LoadHeaderByHeight(ctx context.Context, height uint32) (*BlockHeaderResponse, error) {
	heightCacheKey := fmt.Sprintf("blockheight:%d", height)

	if hash, err := l.cache.Get(ctx, heightCacheKey).Result(); err == nil && hash != "" {
		return l.LoadHeaderByHash(ctx, hash)
	}

	url := fmt.Sprintf("%s/v1/block_header/get/%d/bin", l.junglebusURL, height)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		slog.Debug("Block header not found in JungleBus", "height", height)
		return nil, ErrNotFound
	}

	if resp.StatusCode != 200 {
		slog.Debug("JungleBus error fetching block header", "height", height, "status", resp.StatusCode)
		return nil, fmt.Errorf("junglebus returned status %d", resp.StatusCode)
	}

	header, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	blockHash := resp.Header.Get("X-Block-Hash")

	headerCacheKey := "blockheader:" + blockHash
	l.cache.Set(ctx, headerCacheKey, header, 0)

	l.cache.Set(ctx, heightCacheKey, blockHash, cacheTTL)

	return &BlockHeaderResponse{
		Header: header,
		Hash:   blockHash,
	}, nil
}

func (l *JungleBusLoader) LoadTipHeader(ctx context.Context) (*BlockHeaderResponse, error) {
	cacheKey := "chaintip:header"

	if cachedHeader, err := l.cache.Get(ctx, cacheKey).Bytes(); err == nil && len(cachedHeader) > 0 {
		cachedHash, _ := l.cache.Get(ctx, "chaintip:hash").Result()
		cachedHeight, _ := l.cache.Get(ctx, "chaintip:height").Uint64()
		return &BlockHeaderResponse{
			Header: cachedHeader,
			Hash:   cachedHash,
			Height: uint32(cachedHeight),
		}, nil
	}

	url := fmt.Sprintf("%s/v1/block_header/tip/bin", l.junglebusURL)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		slog.Debug("JungleBus error fetching chain tip", "status", resp.StatusCode)
		return nil, fmt.Errorf("junglebus returned status %d", resp.StatusCode)
	}

	header, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	blockHash := resp.Header.Get("X-Block-Hash")
	heightStr := resp.Header.Get("X-Block-Height")

	if heightStr == "" {
		return nil, fmt.Errorf("missing X-Block-Height header in tip response")
	}

	height, err := strconv.ParseUint(heightStr, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid height in tip response: %w", err)
	}

	if blockHash != "" {
		headerCacheKey := "blockheader:" + blockHash
		l.cache.Set(ctx, headerCacheKey, header, 0)

		heightCacheKey := fmt.Sprintf("blockheight:%d", height)
		l.cache.Set(ctx, heightCacheKey, blockHash, cacheTTL)
	}

	l.cache.Set(ctx, cacheKey, header, 30*time.Second)
	l.cache.Set(ctx, "chaintip:hash", blockHash, 30*time.Second)
	l.cache.Set(ctx, "chaintip:height", height, 30*time.Second)

	return &BlockHeaderResponse{
		Header: header,
		Hash:   blockHash,
		Height: uint32(height),
	}, nil
}

func (l *JungleBusLoader) LoadChainHeight(ctx context.Context) (uint32, error) {
	if cachedHeight, err := l.cache.Get(ctx, "chaintip:height").Uint64(); err == nil {
		return uint32(cachedHeight), nil
	}

	tipHeader, err := l.LoadTipHeader(ctx)
	if err != nil {
		return 0, err
	}

	return tipHeader.Height, nil
}
