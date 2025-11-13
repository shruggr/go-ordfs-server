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
	ParentOutpoint  *transaction.Outpoint
}

type Request struct {
	Outpoint *transaction.Outpoint
	Txid     *chainhash.Hash
	Seq      *int
	Content  bool
	Map      bool
	Output   bool
	Parent   bool
}

type Resolution struct {
	Origin   *transaction.Outpoint
	Current  *transaction.Outpoint
	Content  *transaction.Outpoint
	Map      *transaction.Outpoint
	Parent   *transaction.Outpoint
	Sequence int
}

type LoadRequest struct {
	Outpoint         *transaction.Outpoint
	Origin           *transaction.Outpoint
	Content          *transaction.Outpoint
	Map              *transaction.Outpoint
	Output           *transaction.Outpoint
	Parent           *transaction.Outpoint
	Sequence         *int
	LoadContentBytes bool
}

type Response struct {
	Outpoint      *transaction.Outpoint
	Origin        *transaction.Outpoint
	ContentType   string
	Content       []byte
	ContentLength int
	Map           string // JSON string
	Sequence      int
	Output        []byte
	Parent        *transaction.Outpoint
}

type ForwardCrawlRequest struct {
	Origin          *transaction.Outpoint
	StartOutpoint   *transaction.Outpoint
	StartSeq        int
	TargetSeq       int
	ParentOutpoints map[string]bool
}

type ForwardCrawlResponse struct {
	Outpoint    *transaction.Outpoint
	Sequence    int
	ParentFound bool
}

type StreamRequest struct {
	Outpoint   *transaction.Outpoint
	RangeStart *int64
	RangeEnd   *int64
}

type StreamResponse struct {
	Origin        *transaction.Outpoint
	ContentType   string
	BytesWritten  int64
	FinalSequence int
	StreamEnded   bool
}
