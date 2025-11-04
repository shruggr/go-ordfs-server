package loader

import (
	"context"
	"errors"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
)

var (
	ErrNotFound = errors.New("not found")
)

type Loader interface {
	LoadTx(ctx context.Context, txid string) (*transaction.Transaction, error)
	LoadOutput(ctx context.Context, outpoint *transaction.Outpoint) (*transaction.TransactionOutput, error)
	GetSpend(ctx context.Context, outpoint string) (*chainhash.Hash, error)
}
