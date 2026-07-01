package coordination

import (
	"crypto/md5"
	"encoding/hex"
	"strconv"
	"testing"
)

func TestCalculatePartitionInRange(t *testing.T) {
	pm := NewPartitionManager(64, "inst-a")
	ids := []string{"", "a", "cmon123", "clx8yz0aq0000abcd1234efgh", "another-monitor-id"}
	for _, id := range ids {
		p := pm.CalculatePartition(id)
		if p < 0 || p >= 64 {
			t.Errorf("CalculatePartition(%q) = %d, want in [0, 64)", id, p)
		}
	}
}

// TestCalculatePartitionMatchesSQLSemantics recomputes the partition the way
// the Postgres expression in PartitionSQLExpr does — hex md5 string, first 8
// chars, parsed as a signed 32-bit value, double-modulo — and requires the Go
// implementation to agree. If CalculatePartition and PartitionSQLExpr ever
// drift, monitors would silently stop being scheduled.
func TestCalculatePartitionMatchesSQLSemantics(t *testing.T) {
	const count = 64
	pm := NewPartitionManager(count, "inst-a")

	ids := []string{"", "a", "clx8yz0aq0000abcd1234efgh", "monitor-with-high-bit"}
	// Include generated-looking IDs to get hash values with the sign bit set.
	for i := 0; i < 100; i++ {
		ids = append(ids, "cmon"+strconv.Itoa(i))
	}

	for _, id := range ids {
		// Postgres: ('x' || substr(md5(id), 1, 8))::bit(32)::int
		sum := md5.Sum([]byte(id))
		hexStr := hex.EncodeToString(sum[:])[:8]
		u, err := strconv.ParseUint(hexStr, 16, 32)
		if err != nil {
			t.Fatalf("parse %q: %v", hexStr, err)
		}
		v := int32(uint32(u))
		want := int((v%count + count) % count)

		if got := pm.CalculatePartition(id); got != want {
			t.Errorf("CalculatePartition(%q) = %d, SQL semantics give %d", id, got, want)
		}
	}
}

func TestNewPartitionManagerGuardsZeroCount(t *testing.T) {
	pm := NewPartitionManager(0, "inst-a")
	// Must not panic (modulo by zero) and must stay in range.
	if p := pm.CalculatePartition("x"); p != 0 {
		t.Errorf("CalculatePartition with clamped count = %d, want 0", p)
	}
}

func TestRecalculateOwnershipSplitsAllPartitions(t *testing.T) {
	const count = 8
	a := NewPartitionManager(count, "inst-a")
	b := NewPartitionManager(count, "inst-b")

	instances := []string{"inst-a", "inst-b"}
	a.RecalculateOwnership(instances)
	b.RecalculateOwnership(instances)

	for p := 0; p < count; p++ {
		ownedByA := a.IsOwned(p)
		ownedByB := b.IsOwned(p)
		if ownedByA == ownedByB {
			t.Errorf("partition %d: ownedByA=%v ownedByB=%v, want exactly one owner", p, ownedByA, ownedByB)
		}
	}
}

func TestRecalculateOwnershipDelta(t *testing.T) {
	pm := NewPartitionManager(4, "inst-a")

	delta := pm.RecalculateOwnership([]string{"inst-a"})
	if len(delta.Gained) != 4 || len(delta.Lost) != 0 {
		t.Fatalf("solo instance: gained=%v lost=%v, want all 4 gained", delta.Gained, delta.Lost)
	}

	// Second instance joins: inst-a keeps even partitions, loses odd ones.
	delta = pm.RecalculateOwnership([]string{"inst-a", "inst-b"})
	if len(delta.Gained) != 0 {
		t.Errorf("after join: gained=%v, want none", delta.Gained)
	}
	if len(delta.Lost) != 2 {
		t.Errorf("after join: lost=%v, want 2 partitions", delta.Lost)
	}

	// Unchanged membership must be a no-op delta.
	delta = pm.RecalculateOwnership([]string{"inst-a", "inst-b"})
	if len(delta.Gained) != 0 || len(delta.Lost) != 0 {
		t.Errorf("unchanged membership: gained=%v lost=%v, want empty", delta.Gained, delta.Lost)
	}
}

func TestRecalculateOwnershipInstanceRemoved(t *testing.T) {
	pm := NewPartitionManager(4, "inst-a")
	pm.RecalculateOwnership([]string{"inst-a", "inst-b"})

	// This instance dropped out of the membership list (e.g. its lease
	// expired): it must own nothing and report everything as lost.
	delta := pm.RecalculateOwnership([]string{"inst-b"})
	if len(delta.Gained) != 0 {
		t.Errorf("removed instance: gained=%v, want none", delta.Gained)
	}
	if len(pm.GetOwnedPartitions()) != 0 {
		t.Errorf("removed instance: owned=%v, want none", pm.GetOwnedPartitions())
	}
	if len(delta.Lost) == 0 {
		t.Error("removed instance: want lost partitions reported")
	}
}
