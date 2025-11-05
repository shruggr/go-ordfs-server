package ordfs

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/redis/go-redis/v9"
	"github.com/shruggr/go-ordfs-server/loader"
)

// Mock loader that reads transactions from testdata/*.hex files
type mockLoader struct {
	spends map[string]*chainhash.Hash
}

func newMockLoader() *mockLoader {
	return &mockLoader{
		spends: make(map[string]*chainhash.Hash),
	}
}

func (m *mockLoader) LoadTx(ctx context.Context, txid string) (*transaction.Transaction, error) {
	filename := fmt.Sprintf("testdata/%s.hex", txid)
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, loader.ErrNotFound
	}

	txHex := strings.TrimSpace(string(data))
	txBytes, err := hex.DecodeString(txHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode tx hex: %w", err)
	}

	tx, err := transaction.NewTransactionFromBytes(txBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse tx: %w", err)
	}

	return tx, nil
}

func (m *mockLoader) LoadOutput(ctx context.Context, outpoint *transaction.Outpoint) (*transaction.TransactionOutput, error) {
	tx, err := m.LoadTx(ctx, outpoint.Txid.String())
	if err != nil {
		return nil, err
	}

	if int(outpoint.Index) >= len(tx.Outputs) {
		return nil, fmt.Errorf("output index %d out of range", outpoint.Index)
	}

	return tx.Outputs[outpoint.Index], nil
}

func (m *mockLoader) GetSpend(ctx context.Context, outpoint string) (*chainhash.Hash, error) {
	if spend, ok := m.spends[outpoint]; ok {
		return spend, nil
	}
	return nil, nil // unspent
}

func (m *mockLoader) addSpend(outpoint string, spendTxid string) error {
	hash, err := chainhash.NewHashFromHex(spendTxid)
	if err != nil {
		return err
	}
	m.spends[outpoint] = hash
	return nil
}

// Test chain data from scenarios.md
const (
	origin = "668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0"
	seq1   = "d8fad9be1306bf7c8bc99195f39370c6f0c02cdb72ad7af6d0fc5f680fdcd0ea_0"
	seq2   = "edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0"
	seq3   = "e8c2b7edc2da9be5e0262f861e0255347f0cab931b20164590c85a7577644ce2_0"
)

func intPtr(i int) *int {
	return &i
}

func setupTestChain(t *testing.T) (*mockLoader, *redis.Client, *Ordfs) {
	mr := miniredis.RunT(t)

	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	ldr := newMockLoader()

	// Set up spend chain - transactions are loaded automatically from testdata/{txid}.hex
	ldr.addSpend(origin, "d8fad9be1306bf7c8bc99195f39370c6f0c02cdb72ad7af6d0fc5f680fdcd0ea")
	ldr.addSpend(seq1, "edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b")
	ldr.addSpend(seq2, "e8c2b7edc2da9be5e0262f861e0255347f0cab931b20164590c85a7577644ce2")

	ordfs := New(ldr, redisClient)

	return ldr, redisClient, ordfs
}

// Scenario 1: Forward Crawl from Origin
func TestForwardCrawl(t *testing.T) {
	_, redisClient, ordfs := setupTestChain(t)
	defer redisClient.Close()
	ctx := context.Background()

	t.Run("1.1: Resolve origin (seq=0)", func(t *testing.T) {
		outpoint, _ := transaction.OutpointFromString(origin)

		resolution, err := ordfs.Resolve(ctx, outpoint, 0)
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}

		if resolution.Origin.OrdinalString() != origin {
			t.Errorf("Expected origin %s, got %s", origin, resolution.Origin.OrdinalString())
		}

		if resolution.Current.OrdinalString() != origin {
			t.Errorf("Expected current %s, got %s", origin, resolution.Current.OrdinalString())
		}

		if resolution.Sequence != 0 {
			t.Errorf("Expected sequence 0, got %d", resolution.Sequence)
		}

		if resolution.Content == nil {
			t.Error("Expected content outpoint, got nil")
		}
	})

	t.Run("1.2: Resolve seq=1 (partial forward crawl)", func(t *testing.T) {
		outpoint, _ := transaction.OutpointFromString(origin)

		resolution, err := ordfs.Resolve(ctx, outpoint, 1)
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}

		if resolution.Origin.OrdinalString() != origin {
			t.Errorf("Expected origin %s, got %s", origin, resolution.Origin.OrdinalString())
		}

		if resolution.Current.OrdinalString() != seq1 {
			t.Errorf("Expected current %s, got %s", seq1, resolution.Current.OrdinalString())
		}

		if resolution.Sequence != 1 {
			t.Errorf("Expected sequence 1, got %d", resolution.Sequence)
		}

		if resolution.Content == nil {
			t.Error("Expected content outpoint (from origin), got nil")
		} else if resolution.Content.OrdinalString() != origin {
			t.Errorf("Expected content from origin %s, got %s", origin, resolution.Content.OrdinalString())
		}
	})

	t.Run("1.3: Resolve seq=-1 (full forward crawl to latest)", func(t *testing.T) {
		outpoint, _ := transaction.OutpointFromString(origin)

		resolution, err := ordfs.Resolve(ctx, outpoint, -1)
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}

		if resolution.Origin.OrdinalString() != origin {
			t.Errorf("Expected origin %s, got %s", origin, resolution.Origin.OrdinalString())
		}

		if resolution.Current.OrdinalString() != seq3 {
			t.Errorf("Expected current %s, got %s", seq3, resolution.Current.OrdinalString())
		}

		if resolution.Sequence != 3 {
			t.Errorf("Expected sequence 3, got %d", resolution.Sequence)
		}

		// Content should be from the most recent content revision (seq1 in current test data)
		if resolution.Content == nil {
			t.Error("Expected content outpoint, got nil")
		} else if resolution.Content.OrdinalString() != seq1 {
			t.Errorf("Expected content from seq1 %s, got %s", seq1, resolution.Content.OrdinalString())
		}
	})
}

// Scenario 2: Direct Outpoint (no sequence traversal when seq=nil)
func TestDirectOutpoint(t *testing.T) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer redisClient.Close()

	ldr := newMockLoader()
	ordfs := New(ldr, redisClient)
	ctx := context.Background()

	t.Run("2.1: Request transaction directly (seq=nil)", func(t *testing.T) {
		// Use an outpoint with actual inscription content
		directOutpoint := "e496b2984a6b780a453559125540ec1e1c99154cdbc1cef2d2f6bea37d6dedd9_0"
		outpoint, _ := transaction.OutpointFromString(directOutpoint)

		resp, err := ordfs.Load(ctx, &Request{
			Outpoint: outpoint,
			Seq:      nil, // No seq specified - should treat as direct outpoint
			Content:  true,
			Map:      true,
		})
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		if resp.Outpoint.OrdinalString() != directOutpoint {
			t.Errorf("Expected outpoint %s (self), got %s", directOutpoint, resp.Outpoint.OrdinalString())
		}

		if resp.Sequence != 0 {
			t.Errorf("Expected sequence 0, got %d", resp.Sequence)
		}

		if resp.ContentType == "" {
			t.Error("Expected content to be loaded from inscription")
		}

		if len(resp.Content) == 0 {
			t.Error("Expected content data to be present")
		}
	})
}

// Scenario 3: Backward Crawl from Later Outpoint
func TestBackwardCrawl(t *testing.T) {
	_, redisClient, ordfs := setupTestChain(t)
	defer redisClient.Close()
	ctx := context.Background()

	t.Run("2.1: Resolve from seq 2 outpoint with seq=0", func(t *testing.T) {
		// Start from seq 2 outpoint, request seq 0
		outpoint, _ := transaction.OutpointFromString(seq2)

		resolution, err := ordfs.Resolve(ctx, outpoint, 0)
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}

		if resolution.Origin.OrdinalString() != origin {
			t.Errorf("Expected origin %s, got %s", origin, resolution.Origin.OrdinalString())
		}

		if resolution.Current.OrdinalString() != origin {
			t.Errorf("Expected current %s, got %s", origin, resolution.Current.OrdinalString())
		}

		if resolution.Sequence != 0 {
			t.Errorf("Expected sequence 0, got %d", resolution.Sequence)
		}

		// Verify origin is now cached
		cachedOrigin := redisClient.HGet(ctx, "origins", seq2).Val()
		if cachedOrigin != origin {
			t.Errorf("Expected cached origin %s, got %s", origin, cachedOrigin)
		}
	})

	t.Run("2.2: Resolve from seq 2 outpoint with seq=1", func(t *testing.T) {
		// Origin should be cached from 2.1
		outpoint, _ := transaction.OutpointFromString(seq2)

		resolution, err := ordfs.Resolve(ctx, outpoint, 1)
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}

		if resolution.Current.OrdinalString() != seq1 {
			t.Errorf("Expected current %s, got %s", seq1, resolution.Current.OrdinalString())
		}

		if resolution.Sequence != 1 {
			t.Errorf("Expected sequence 1, got %d", resolution.Sequence)
		}
	})
}

// Scenario 3: Cache Hit Scenarios
func TestCacheHits(t *testing.T) {
	_, redisClient, ordfs := setupTestChain(t)
	defer redisClient.Close()
	ctx := context.Background()

	t.Run("3.1: Second request to same sequence should hit cache", func(t *testing.T) {
		outpoint, _ := transaction.OutpointFromString(origin)

		// First request
		_, err := ordfs.Resolve(ctx, outpoint, 1)
		if err != nil {
			t.Fatalf("First resolve failed: %v", err)
		}

		// Verify data is cached
		originKey := fmt.Sprintf("seq:%s", origin)
		cached := redisClient.ZRangeByScore(ctx, originKey, &redis.ZRangeBy{
			Min: "1",
			Max: "1",
		}).Val()

		if len(cached) == 0 {
			t.Error("Expected seq=1 to be cached")
		}

		// Second request should use cache
		resolution, err := ordfs.Resolve(ctx, outpoint, 1)
		if err != nil {
			t.Fatalf("Second resolve failed: %v", err)
		}

		if resolution.Sequence != 1 {
			t.Errorf("Expected sequence 1, got %d", resolution.Sequence)
		}
	})
}

// Scenario 4: Specific Sequence Resolution
func TestSpecificSequences(t *testing.T) {
	_, redisClient, ordfs := setupTestChain(t)
	defer redisClient.Close()
	ctx := context.Background()

	// Populate cache with scenario 1 first
	outpoint, _ := transaction.OutpointFromString(origin)
	ordfs.Resolve(ctx, outpoint, -1)

	t.Run("4.1: Request seq=2 from origin", func(t *testing.T) {
		resolution, err := ordfs.Resolve(ctx, outpoint, 2)
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}

		if resolution.Sequence != 2 {
			t.Errorf("Expected sequence 2, got %d", resolution.Sequence)
		}

		if resolution.Current.OrdinalString() != seq2 {
			t.Errorf("Expected current %s, got %s", seq2, resolution.Current.OrdinalString())
		}
	})

	t.Run("4.2: Request seq=3 from origin", func(t *testing.T) {
		resolution, err := ordfs.Resolve(ctx, outpoint, 3)
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}

		if resolution.Sequence != 3 {
			t.Errorf("Expected sequence 3, got %d", resolution.Sequence)
		}

		if resolution.Current.OrdinalString() != seq3 {
			t.Errorf("Expected current %s, got %s", seq3, resolution.Current.OrdinalString())
		}
	})
}

// Scenario 7: Error Cases
func TestErrorCases(t *testing.T) {
	_, redisClient, ordfs := setupTestChain(t)
	defer redisClient.Close()
	ctx := context.Background()

	t.Run("7.1: Sequence beyond chain end", func(t *testing.T) {
		outpoint, _ := transaction.OutpointFromString(origin)

		_, err := ordfs.Resolve(ctx, outpoint, 10)
		if err == nil {
			t.Error("Expected error for sequence beyond chain end")
		}
	})

	t.Run("7.2: Non-existent outpoint", func(t *testing.T) {
		outpoint, _ := transaction.OutpointFromString("deadbeef00000000000000000000000000000000000000000000000000000000_0")

		_, err := ordfs.Resolve(ctx, outpoint, 0)
		if err == nil {
			t.Error("Expected error for non-existent outpoint")
		}
	})
}
