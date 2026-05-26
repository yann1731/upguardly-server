package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"upguardly-backend/internal/config"
)

const (
	instanceKeyPrefix = "/upguardly/schedulers/instances/"
)

type InstanceMetadata struct {
	InstanceID string    `json:"instance_id"`
	StartedAt  time.Time `json:"started_at"`
}

type MembershipEvent struct {
	Instances []string
}

type Coordinator struct {
	client     *clientv3.Client
	instanceID string
	leaseID    clientv3.LeaseID
	leaseTTL   time.Duration

	mu        sync.RWMutex
	instances []string

	stopCh chan struct{}
	doneCh chan struct{}
}

func NewCoordinator(cfg config.EtcdConfig, instanceID string, leaseTTL time.Duration) (*Coordinator, error) {
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: cfg.DialTimeout,
		Username:    cfg.Username,
		Password:    cfg.Password,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create etcd client: %w", err)
	}

	return &Coordinator{
		client:     client,
		instanceID: instanceID,
		leaseTTL:   leaseTTL,
		instances:  []string{},
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}, nil
}

func (c *Coordinator) Register(ctx context.Context) error {
	ttlSeconds := int64(c.leaseTTL.Seconds())
	if ttlSeconds < 5 {
		ttlSeconds = 5
	}

	leaseResp, err := c.client.Grant(ctx, ttlSeconds)
	if err != nil {
		return fmt.Errorf("failed to create lease: %w", err)
	}
	c.leaseID = leaseResp.ID

	metadata := InstanceMetadata{
		InstanceID: c.instanceID,
		StartedAt:  time.Now(),
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	key := instanceKeyPrefix + c.instanceID
	_, err = c.client.Put(ctx, key, string(metadataJSON), clientv3.WithLease(c.leaseID))
	if err != nil {
		return fmt.Errorf("failed to register instance: %w", err)
	}

	go c.keepAlive()

	log.Printf("Registered instance %s with etcd (lease TTL: %v)", c.instanceID, c.leaseTTL)
	return nil
}

func (c *Coordinator) keepAlive() {
	defer close(c.doneCh)

	keepAliveCh, err := c.client.KeepAlive(context.Background(), c.leaseID)
	if err != nil {
		log.Printf("Failed to start lease keepalive: %v", err)
		return
	}

	for {
		select {
		case <-c.stopCh:
			return
		case resp, ok := <-keepAliveCh:
			if !ok {
				log.Printf("Lease keepalive channel closed, attempting to re-register")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := c.Register(ctx); err != nil {
					log.Printf("Failed to re-register: %v", err)
				}
				cancel()
				return
			}
			if resp == nil {
				continue
			}
		}
	}
}

func (c *Coordinator) Deregister(ctx context.Context) error {
	close(c.stopCh)

	select {
	case <-c.doneCh:
	case <-time.After(5 * time.Second):
		log.Printf("Timeout waiting for keepalive to stop")
	}

	key := instanceKeyPrefix + c.instanceID
	_, err := c.client.Delete(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to delete instance key: %w", err)
	}

	if c.leaseID != 0 {
		_, err = c.client.Revoke(ctx, c.leaseID)
		if err != nil {
			log.Printf("Failed to revoke lease: %v", err)
		}
	}

	log.Printf("Deregistered instance %s from etcd", c.instanceID)
	return nil
}

func (c *Coordinator) WatchInstances(ctx context.Context, eventCh chan<- MembershipEvent) error {
	if err := c.refreshInstances(ctx); err != nil {
		return fmt.Errorf("failed to get initial instances: %w", err)
	}

	eventCh <- MembershipEvent{Instances: c.GetInstances()}

	go c.watchLoop(ctx, eventCh)

	return nil
}

func (c *Coordinator) refreshInstances(ctx context.Context) error {
	resp, err := c.client.Get(ctx, instanceKeyPrefix, clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("failed to list instances: %w", err)
	}

	instances := make([]string, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var metadata InstanceMetadata
		if err := json.Unmarshal(kv.Value, &metadata); err != nil {
			log.Printf("Failed to parse instance metadata: %v", err)
			continue
		}
		instances = append(instances, metadata.InstanceID)
	}

	sort.Strings(instances)

	c.mu.Lock()
	c.instances = instances
	c.mu.Unlock()

	return nil
}

func (c *Coordinator) watchLoop(ctx context.Context, eventCh chan<- MembershipEvent) {
	watchCh := c.client.Watch(ctx, instanceKeyPrefix, clientv3.WithPrefix())

	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-watchCh:
			if !ok {
				log.Printf("Watch channel closed, restarting watch")
				watchCh = c.client.Watch(ctx, instanceKeyPrefix, clientv3.WithPrefix())
				continue
			}

			if resp.Err() != nil {
				log.Printf("Watch error: %v", resp.Err())
				continue
			}

			hasChange := false
			for _, ev := range resp.Events {
				log.Printf("Instance change detected: %s %s", ev.Type, string(ev.Kv.Key))
				hasChange = true
			}

			if hasChange {
				if err := c.refreshInstances(ctx); err != nil {
					log.Printf("Failed to refresh instances: %v", err)
					continue
				}

				select {
				case eventCh <- MembershipEvent{Instances: c.GetInstances()}:
				default:
					log.Printf("Event channel full, dropping membership event")
				}
			}
		}
	}
}

func (c *Coordinator) GetInstances() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]string, len(c.instances))
	copy(result, c.instances)
	return result
}

func (c *Coordinator) GetInstanceIndex() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for i, id := range c.instances {
		if id == c.instanceID {
			return i
		}
	}
	return -1
}

func (c *Coordinator) GetInstanceCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.instances)
}

func (c *Coordinator) Close() error {
	return c.client.Close()
}
