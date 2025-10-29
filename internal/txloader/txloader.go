package txloader

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/redis/go-redis/v9"
)

var (
	ErrNotFound = errors.New("not found")
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

	t.cache.Set(ctx, cacheKey, rawtx, 0)

	return tx, nil
}

func (t *TxLoader) GetSpend(ctx context.Context, outpoint string) (*chainhash.Hash, error) {
	url := fmt.Sprintf("%s/v1/txo/spend/%s", t.junglebusURL, outpoint)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
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
		return nil, ErrNotFound
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("junglebus returned status %d", resp.StatusCode)
	}

	rawtx, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return rawtx, nil
}
