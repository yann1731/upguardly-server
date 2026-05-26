package coordination

import (
	"hash/crc32"
	"sort"
	"sync"
)

type PartitionManager struct {
	partitionCount int
	instanceID     string

	mu              sync.RWMutex
	ownedPartitions map[int]bool
	instanceIndex   int
	instanceCount   int
}

type PartitionDelta struct {
	Gained []int
	Lost   []int
}

func NewPartitionManager(partitionCount int, instanceID string) *PartitionManager {
	return &PartitionManager{
		partitionCount:  partitionCount,
		instanceID:      instanceID,
		ownedPartitions: make(map[int]bool),
		instanceIndex:   -1,
		instanceCount:   0,
	}
}

func (pm *PartitionManager) CalculatePartition(monitorID string) int {
	hash := crc32.ChecksumIEEE([]byte(monitorID))
	return int(hash) % pm.partitionCount
}

func (pm *PartitionManager) RecalculateOwnership(instances []string) PartitionDelta {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	instanceIndex := -1
	for i, id := range instances {
		if id == pm.instanceID {
			instanceIndex = i
			break
		}
	}

	if instanceIndex == -1 {
		lost := make([]int, 0, len(pm.ownedPartitions))
		for p := range pm.ownedPartitions {
			lost = append(lost, p)
		}
		sort.Ints(lost)

		pm.ownedPartitions = make(map[int]bool)
		pm.instanceIndex = -1
		pm.instanceCount = len(instances)

		return PartitionDelta{Lost: lost}
	}

	instanceCount := len(instances)
	newOwned := make(map[int]bool)

	for p := 0; p < pm.partitionCount; p++ {
		if p%instanceCount == instanceIndex {
			newOwned[p] = true
		}
	}

	var gained, lost []int

	for p := range newOwned {
		if !pm.ownedPartitions[p] {
			gained = append(gained, p)
		}
	}

	for p := range pm.ownedPartitions {
		if !newOwned[p] {
			lost = append(lost, p)
		}
	}

	sort.Ints(gained)
	sort.Ints(lost)

	pm.ownedPartitions = newOwned
	pm.instanceIndex = instanceIndex
	pm.instanceCount = instanceCount

	return PartitionDelta{Gained: gained, Lost: lost}
}

func (pm *PartitionManager) IsOwned(partition int) bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.ownedPartitions[partition]
}

func (pm *PartitionManager) IsMonitorOwned(monitorID string) bool {
	partition := pm.CalculatePartition(monitorID)
	return pm.IsOwned(partition)
}

func (pm *PartitionManager) GetOwnedPartitions() []int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]int, 0, len(pm.ownedPartitions))
	for p := range pm.ownedPartitions {
		result = append(result, p)
	}
	sort.Ints(result)
	return result
}

func (pm *PartitionManager) GetInstanceInfo() (index, count int) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.instanceIndex, pm.instanceCount
}

func (pm *PartitionManager) PartitionCount() int {
	return pm.partitionCount
}
