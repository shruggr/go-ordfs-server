package txloader

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/redis/go-redis/v9"
)

var (
	ErrNotFound = errors.New("not found")
)

const (
	cacheTTL = 365 * 24 * time.Hour // 1 year - makes cache volatile for Redis eviction
)

type TxLoader struct {
	cache        *redis.Client
	junglebusURL string
}

func New(cache *redis.Client, junglebusURL string) *TxLoader {
	return &TxLoader{
		cache:        cache,
		junglebusURL: junglebusURL,
	}
}

func (t *TxLoader) txKey(txid string) string {
	return "tx:" + txid
}

func (t *TxLoader) LoadTx(ctx context.Context, txid string) (*transaction.Transaction, error) {
	cacheKey := t.txKey(txid)

	if rawtx, err := t.cache.Get(ctx, cacheKey).Bytes(); err == nil && len(rawtx) > 0 {
		if tx, err := transaction.NewTransactionFromBytes(rawtx); err == nil {
			return tx, nil
		}
		t.cache.Del(ctx, cacheKey)
	}

	rawtx, err := t.loadRemoteRawtx(ctx, txid)
	if err != nil {
		return nil, err
	}

	tx, err := transaction.NewTransactionFromBytes(rawtx)
	if err != nil {
		return nil, fmt.Errorf("malformed transaction: %w", err)
	}

	t.cache.Set(ctx, cacheKey, rawtx, cacheTTL)

	return tx, nil
}

func (t *TxLoader) LoadOutput(ctx context.Context, outpoint *transaction.Outpoint) (*transaction.TransactionOutput, error) {
	cacheKey := fmt.Sprintf("txo:%s", outpoint.OrdinalString())

	if rawOutput, err := t.cache.Get(ctx, cacheKey).Bytes(); err == nil && len(rawOutput) > 0 {
		if output, err := t.parseOutput(rawOutput); err == nil {
			return output, nil
		}
		t.cache.Del(ctx, cacheKey)
	}

	url := fmt.Sprintf("%s/v1/txo/get/%s", t.junglebusURL, outpoint.OrdinalString())

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

	output, err := t.parseOutput(rawOutput)
	if err != nil {
		return nil, fmt.Errorf("malformed output: %w", err)
	}

	t.cache.Set(ctx, cacheKey, rawOutput, cacheTTL)

	return output, nil
}

func (t *TxLoader) parseOutput(bytes []byte) (*transaction.TransactionOutput, error) {
	if len(bytes) < 8 {
		return nil, fmt.Errorf("output too short: %d bytes", len(bytes))
	}

	lockingScript := script.Script(bytes[8:])

	return &transaction.TransactionOutput{
		Satoshis:      binary.LittleEndian.Uint64(bytes[0:8]),
		LockingScript: &lockingScript,
	}, nil
}

func (t *TxLoader) GetSpend(ctx context.Context, outpoint string) (*chainhash.Hash, error) {
	url := fmt.Sprintf("%s/v1/txo/spend/%s", t.junglebusURL, outpoint)
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

func (t *TxLoader) loadRemoteRawtx(ctx context.Context, txid string) ([]byte, error) {
	url := fmt.Sprintf("%s/v1/transaction/get/%s/bin", t.junglebusURL, txid)

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
