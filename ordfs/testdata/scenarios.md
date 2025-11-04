# Test Scenarios for Ordinal Tracking

## Test Chain: react-onchain versioning system

**Origin:** `668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0`

### Chain Overview
- **Length:** 4 outpoints (sequences 0-3)
- **Content:** Only at origin (seq 0) - "video-test" (text/plain)
- **MAP Protocol:** Cumulative versioning data across all sequences
- **Pattern:** Origin has content, subsequent spends only update MAP

### Outpoint Chain
```
Seq 0 (Origin):  668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
  ↓ spent by
Seq 1:           d8fad9be1306bf7c8bc99195f39370c6f0c02cdb72ad7af6d0fc5f680fdcd0ea_0
  ↓ spent by
Seq 2:           edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0
  ↓ spent by
Seq 3 (Latest):  e8c2b7edc2da9be5e0262f861e0255347f0cab931b20164590c85a7577644ce2_0
```

---

## Scenario 1: Forward Crawl from Origin

**Objective:** Test crawling forward from the origin to discover the full chain

### Setup
- Clear Redis cache for this chain
- Start at origin: `668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0`

### Test Cases

#### 1.1: Resolve origin (seq=0)
```
Request: seq=0
Expected:
  - Origin: 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
  - Current: 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
  - Sequence: 0
  - Content: "video-test"
  - ContentType: "text/plain"
  - MAP: {"app":"react-onchain","type":"version"}
```

#### 1.2: Resolve seq=1 (partial forward crawl)
```
Request: seq=1
Expected:
  - Origin: 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
  - Current: d8fad9be1306bf7c8bc99195f39370c6f0c02cdb72ad7af6d0fc5f680fdcd0ea_0
  - Sequence: 1
  - Content: "video-test" (from origin)
  - ContentType: "text/plain"
  - MAP: {"app":"react-onchain","type":"version","version.1.0.0":"..."}
  - MAP should be MERGED from seq 0 and seq 1
  - Validates that forward crawling one step succeeds
```

#### 1.3: Resolve seq=-1 (full forward crawl to latest)
```
Request: seq=-1
Expected:
  - Origin: 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
  - Current: e8c2b7edc2da9be5e0262f861e0255347f0cab931b20164590c85a7577644ce2_0
  - Sequence: 3
  - Content: "video-test" (from origin)
  - MAP: merged data including all version keys (version.1.0.0, version.1.0.1, version.1.0.2)
  - Should crawl from cached seq 1 to end of chain
  - Should NOT be cached (no-cache headers)
  - Cache-Control: no-cache, no-store, must-revalidate
```

---

## Scenario 2: No Sequence (seq=nil)

**Objective:** Test fast path with no ordinal tracking

### Test Cases

#### 2.1: Request transaction with no seq parameter
```
Request:
  Outpoint: edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0
  seq: nil (not specified)

Process:
  - NO backward crawl
  - NO origin lookup
  - NO forward crawl
  - Just return the transaction itself

Expected:
  - Origin: edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0 (self)
  - Current: edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0 (self)
  - Sequence: 0
  - Content: From this specific outpoint (not origin)
  - MAP: Only MAP data from this specific outpoint
  - FAST PATH: No crawling of any kind
```

---

## Scenario 3: Backward Crawl from Later Outpoint

**Objective:** Test crawling backward from a later outpoint to find origin

### Setup
- Clear Redis cache
- Start at seq 2 outpoint: `edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0`

### Test Cases

#### 3.1: Resolve from seq 2 outpoint with seq=0 (find origin)
```
Request:
  Outpoint: edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0 (seq 2 in chain)
  seq: 0

Process:
  1. backwardCrawl should find origin
  2. Should create chain entries for all 3 outpoints encountered
  3. Should cache origin mapping
  4. Return origin at sequence 0

Expected:
  - Origin: 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
  - Current: 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
  - Sequence: 0
  - Content: "video-test"
```

#### 3.2: Resolve from seq 2 outpoint with seq=1
```
Request:
  Outpoint: edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0
  seq: 1

Expected:
  - Origin should be cached from 2.1
  - Current: d8fad9be1306bf7c8bc99195f39370c6f0c02cdb72ad7af6d0fc5f680fdcd0ea_0
  - Sequence: 1
```

---

## Scenario 4: Cache Hit Scenarios

**Objective:** Test that caching works correctly

### Test Cases

#### 3.1: Second request to same sequence
```
Setup:
  1. Request: origin, seq=1
  2. Clear loader call counter
  3. Request: origin, seq=1 again

Expected:
  - Second request should hit cache
  - No JungleBus API calls
  - Identical response to first request
```

#### 3.2: seq=-1 should NOT cache
```
Setup:
  1. Request: origin, seq=-1
  2. Request: origin, seq=-1 again

Expected:
  - Both requests should execute full resolution
  - Cache-Control: no-cache headers
```

---

## Scenario 5: Specific Sequence Resolution

**Objective:** Verify resolution of specific sequence numbers (not seq=-1)

### Test Cases

#### 4.1: Request seq=2 from origin
```
Starting from: 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
Request: seq=2
Expected:
  - Absolute seq: 2
  - Current: edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0
  - Content: "video-test" (from origin)
  - MAP: includes version.1.0.0 and version.1.0.1
  - Should use cached data from Scenario 1
```

#### 4.2: Request seq=3 from origin
```
Starting from: 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
Request: seq=3
Expected:
  - Absolute seq: 3
  - Current: e8c2b7edc2da9be5e0262f861e0255347f0cab931b20164590c85a7577644ce2_0
  - Content: "video-test" (from origin)
  - MAP: includes all three version keys
  - Should use cached data from Scenario 1
```

---

## Scenario 6: MAP Merging

**Objective:** Verify MAP data is correctly merged across sequences

### Test Cases

#### 5.1: MAP accumulation
```
Seq 0 MAP: {"app":"react-onchain","type":"version"}
Seq 1 MAP: adds "version.1.0.0"
Seq 2 MAP: adds "version.1.0.1"
Seq 3 MAP: adds "version.1.0.2"

When requesting seq=3 with map=true:
  - Should have all 4 MAP keys
  - Later values should override earlier ones (if keys collide)
```

#### 5.2: MAP caching
```
Request seq=2 with map=true twice
Expected:
  - First request: builds merged map, caches it
  - Second request: retrieves cached merged map
  - Key: "merged:edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0"
```

---

## Scenario 7: Content Resolution

**Objective:** Verify content always resolves to origin

### Test Cases

#### 6.1: Content from later sequence
```
Request: seq=3, content=true
Expected:
  - Content should be "video-test" (from origin seq 0)
  - Content outpoint should be origin
```

#### 6.2: No content in middle of chain
```
Request: d8fad9be1306bf7c8bc99195f39370c6f0c02cdb72ad7af6d0fc5f680fdcd0ea_0, seq=0
Process:
  - This outpoint has no content itself
  - backwardCrawl to origin
  - Content should resolve to origin's content
Expected:
  - Content: "video-test"
  - Content comes from: 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
```

---

## Scenario 8: Error Cases

**Objective:** Test error handling

### Test Cases

#### 7.1: Sequence beyond chain end
```
Request: origin, seq=10
Expected:
  - Error: "target sequence 10 not found (chain ends at 3)"
```

#### 7.2: Non-existent outpoint
```
Request: deadbeef00000000000000000000000000000000000000000000000000000000_0
Expected:
  - Error: "failed to load output" or "not found"
```

---

## Scenario 8: Concurrent Access

**Objective:** Test locking and pub/sub coordination

### Test Cases

#### 8.1: Two simultaneous backward crawls
```
Setup:
  - Clear cache
  - Start two concurrent requests for same outpoint

Process:
  - First request should acquire lock
  - Second request should wait on pub/sub channel
  - First request completes, publishes origin
  - Second request uses published origin

Expected:
  - Both requests complete successfully
  - Only one backward crawl performed
  - Both get same origin
```

---

## Cache Keys Reference

### Redis Keys Used by This Chain

```
origins: hash
  - 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0 → 668fd698...
  - d8fad9be1306bf7c8bc99195f39370c6f0c02cdb72ad7af6d0fc5f680fdcd0ea_0 → 668fd698...
  - edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0 → 668fd698...
  - e8c2b7edc2da9be5e0262f861e0255347f0cab931b20164590c85a7577644ce2_0 → 668fd698...

seq:668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0: sorted set
  - Score 0: 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
  - Score 1: d8fad9be1306bf7c8bc99195f39370c6f0c02cdb72ad7af6d0fc5f680fdcd0ea_0
  - Score 2: edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0
  - Score 3: e8c2b7edc2da9be5e0262f861e0255347f0cab931b20164590c85a7577644ce2_0

rev:668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0: sorted set
  - Score 0: 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
  (only origin has content)

map:668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0: sorted set
  - Score 0: 668fd698178715a2b8941e87a28b7ceb16bdb33656c8570e82d81ccebab3fdbb_0
  - Score 1: d8fad9be1306bf7c8bc99195f39370c6f0c02cdb72ad7af6d0fc5f680fdcd0ea_0
  - Score 2: edaf882952acfbde18dfda14d40cd9744e707166b452ea4c31a4c27fc6938b0b_0
  - Score 3: e8c2b7edc2da9be5e0262f861e0255347f0cab931b20164590c85a7577644ce2_0

parsed:{each outpoint}: hash
  - contentType, content, map fields

merged:{map outpoint}: string (JSON)
  - Cached merged MAP data
```

---

## Test Execution Order

Recommended order for running tests to validate all functionality:

1. **Scenario 1** - Forward crawl (validates basic resolution)
2. **Scenario 2** - No sequence (validates fast path with seq=nil)
3. **Scenario 7** - Content resolution (validates content tracking)
4. **Scenario 6** - MAP merging (validates MAP protocol)
5. **Scenario 3** - Backward crawl (validates origin discovery)
6. **Scenario 5** - Sequence accuracy (validates seq calculation)
7. **Scenario 4** - Caching (validates cache behavior)
8. **Scenario 8** - Error cases (validates error handling)
9. **Scenario 9** - Concurrency (validates locking/pub-sub)
