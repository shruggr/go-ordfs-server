package ordfs

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/redis/go-redis/v9"
)

// Test chain data - child inscription with parent reference
const (
	childOrigin  = "6235f4475fdfb7a89b55d5ca3fefedab72f0390d3a51a5d10eae3f3cc9f94ab9_2"
	parentOrigin = "f099182e168c0a30a9f97556d156c8b6284cfc3f76eca8352807b1ceff29da04_0"
)

func setupParentTest(t *testing.T) (*Ordfs, *redis.Client) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	ldr := newMockLoader()

	// Set up spend chain for parent validation
	// Parent origin spent in intermediate tx
	ldr.addSpend(parentOrigin, "acacef2fac44cf1f2eff3297440ae48c3b5f5d248202f32dc979a3f9b8a64453")
	// Intermediate output spent in child tx
	ldr.addSpend("acacef2fac44cf1f2eff3297440ae48c3b5f5d248202f32dc979a3f9b8a64453_0", "6235f4475fdfb7a89b55d5ca3fefedab72f0390d3a51a5d10eae3f3cc9f94ab9")

	ordfs := New(ldr, redisClient)
	return ordfs, redisClient
}

// Test parent field parsing from inscription
func TestParentParsing(t *testing.T) {
	ordfs, redisClient := setupParentTest(t)
	defer redisClient.Close()
	ctx := context.Background()

	t.Run("Parse inscription with 32-byte parent field", func(t *testing.T) {
		childOutpoint, _ := transaction.OutpointFromString(childOrigin)

		resp, err := ordfs.loadAndParse(ctx, childOutpoint, true)
		if err != nil {
			t.Fatalf("Failed to parse inscription: %v", err)
		}

		if resp.Parent == nil {
			t.Fatal("Expected parent field, got nil")
		}

		if resp.Parent.OrdinalString() != parentOrigin {
			t.Errorf("Expected parent %s, got %s", parentOrigin, resp.Parent.OrdinalString())
		}
	})
}

// Test parent field in response (when parseParent is enabled)
func TestParentFieldParsing(t *testing.T) {
	ordfs, redisClient := setupParentTest(t)
	defer redisClient.Close()
	ctx := context.Background()

	t.Run("loadAndParse extracts parent field", func(t *testing.T) {
		childOutpoint, _ := transaction.OutpointFromString(childOrigin)

		resp, err := ordfs.loadAndParse(ctx, childOutpoint, true)
		if err != nil {
			t.Fatalf("loadAndParse failed: %v", err)
		}

		if resp.Parent == nil {
			t.Fatal("Expected parent field in response, got nil")
		}

		if resp.Parent.OrdinalString() != parentOrigin {
			t.Errorf("Expected parent %s, got %s", parentOrigin, resp.Parent.OrdinalString())
		}
	})
}

// Test parent data stored in Redis during crawls
func TestParentRedisStorage(t *testing.T) {
	ordfs, redisClient := setupParentTest(t)
	defer redisClient.Close()
	ctx := context.Background()

	t.Run("Backward crawl stores parent in sorted set", func(t *testing.T) {
		childOutpoint, _ := transaction.OutpointFromString(childOrigin)

		origin, err := ordfs.backwardCrawl(ctx, childOutpoint)
		if err != nil {
			t.Fatalf("Backward crawl failed: %v", err)
		}

		parentKey := "parents:" + origin.OrdinalString()
		members := ordfs.cache.ZRangeByScore(ctx, parentKey, &redis.ZRangeBy{
			Min: "0",
			Max: "0",
		}).Val()

		if len(members) == 0 {
			t.Error("Expected parent data at seq 0, found none")
		} else if members[0] != childOrigin {
			t.Errorf("Expected parent data to point to %s, got %s", childOrigin, members[0])
		}
	})

	t.Run("Parent data persists in chain entry", func(t *testing.T) {
		childOutpoint, _ := transaction.OutpointFromString(childOrigin)

		resolution, err := ordfs.Resolve(ctx, childOutpoint, 0)
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}

		parentKey := "parents:" + resolution.Origin.OrdinalString()
		count := ordfs.cache.ZCard(ctx, parentKey).Val()

		if count == 0 {
			t.Error("Expected parent data in sorted set after resolve")
		}
	})
}

// Test that Resolve returns parent outpoint from Redis
func TestResolveReturnsParent(t *testing.T) {
	ordfs, redisClient := setupParentTest(t)
	defer redisClient.Close()
	ctx := context.Background()

	childOutpoint, _ := transaction.OutpointFromString(childOrigin)
	seq := 0

	resolution, err := ordfs.Resolve(ctx, childOutpoint, seq)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if resolution.Parent == nil {
		t.Fatal("Resolve did not return a parent - parent data not in Redis")
	}

	// Resolve should return the outpoint where the parent was inscribed
	if resolution.Parent.OrdinalString() != childOrigin {
		t.Errorf("Expected parent outpoint %s, got %s", childOrigin, resolution.Parent.OrdinalString())
	}
}

// Test that loadAndParse extracts parent field from inscription
func TestLoadAndParseExtractsParent(t *testing.T) {
	ordfs, redisClient := setupParentTest(t)
	defer redisClient.Close()
	ctx := context.Background()

	childOutpoint, _ := transaction.OutpointFromString(childOrigin)

	resp, err := ordfs.loadAndParse(ctx, childOutpoint, true)
	if err != nil {
		t.Fatalf("loadAndParse failed: %v", err)
	}

	if resp.Parent == nil {
		t.Fatal("loadAndParse did not extract parent field")
	}

	// Should extract the parent origin from the inscription
	if resp.Parent.OrdinalString() != parentOrigin {
		t.Errorf("Expected parent origin %s, got %s", parentOrigin, resp.Parent.OrdinalString())
	}
}

// Test that validateParent works with our test data
func TestValidateParentWithTestData(t *testing.T) {
	ordfs, redisClient := setupParentTest(t)
	defer redisClient.Close()
	ctx := context.Background()

	childOutpoint, _ := transaction.OutpointFromString(childOrigin)
	parentOutpoint, _ := transaction.OutpointFromString(parentOrigin)

	// Validate that the parent origin is spent in the transaction that created the child outpoint
	validatedOrigin, err := ordfs.validateParent(ctx, parentOutpoint, childOutpoint)
	if err != nil {
		t.Fatalf("validateParent failed: %v", err)
	}

	if validatedOrigin.OrdinalString() != parentOrigin {
		t.Errorf("Expected validated parent origin %s, got %s", parentOrigin, validatedOrigin.OrdinalString())
	}
}

// Test full user flow: Load with parent validation
func TestLoadWithParentValidation(t *testing.T) {
	ordfs, redisClient := setupParentTest(t)
	defer redisClient.Close()
	ctx := context.Background()

	childOutpoint, _ := transaction.OutpointFromString(childOrigin)
	seq := 0

	req := &Request{
		Outpoint: childOutpoint,
		Seq:      &seq,
		Content:  true,
		Parent:   true,
	}

	resp, err := ordfs.Load(ctx, req)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if resp.Parent == nil {
		t.Fatal("Load did not return a validated parent")
	}

	// Load should return the validated parent origin
	if resp.Parent.OrdinalString() != parentOrigin {
		t.Errorf("Expected validated parent origin %s, got %s", parentOrigin, resp.Parent.OrdinalString())
	}
}
