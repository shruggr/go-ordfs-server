package ordfs

import (
	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
)

type ChainEntry struct {
	Outpoint        *transaction.Outpoint
	RelativeSeq     int
	ContentOutpoint *transaction.Outpoint
	MapOutpoint     *transaction.Outpoint
}

type Request struct {
	Outpoint *transaction.Outpoint
	Txid     *chainhash.Hash
	Seq      *int
	Content  bool
	Map      bool
	Output   bool
}

type Resolution struct {
	Origin   *transaction.Outpoint
	Current  *transaction.Outpoint
	Content  *transaction.Outpoint
	Map      *transaction.Outpoint
	Sequence int
}

type LoadRequest struct {
	Outpoint *transaction.Outpoint
	Origin   *transaction.Outpoint
	Content  *transaction.Outpoint
	Map      *transaction.Outpoint
	Output   *transaction.Outpoint
	Sequence int
}

type Response struct {
	Outpoint    *transaction.Outpoint
	Origin      *transaction.Outpoint
	ContentType string
	Content     []byte
	Map         map[string]string
	Sequence    int
	Output      []byte
}
