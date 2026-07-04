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

type InstanceMetadata struct {
	InstanceID string    `json:"instance_id"`
	Region     string    `json:"region"`
	StartedAt  time.Time `json:"started_at"`
}

type MembershipEvent struct {
	Instances []string
}

type Coordinator struct {
	client     *clientv3.Client
	instanceID string
	region     string
	// keyPrefix is region-scoped: instances only see (and share partitions
	// with) members of their own region, so each region's pool independently
	// covers every monitor that lists that region. A monitor checked from N
	// regions is intentionally owned once per region.
	keyPrefix string
	leaseTTL  time.Duration

	mu        sync.RWMutex
	leaseID   clientv3.LeaseID
	instances []string

	stopCh   chan struct{}
	stopOnce sync.Once
	doneCh   chan struct{}
}

func NewCoordinator(cfg config.EtcdConfig, instanceID, region string, leaseTTL time.Duration) (*Coordinator, error) {
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
		region:     region,
		keyPrefix:  "/upguardly/schedulers/" + region + "/instances/",
		leaseTTL:   leaseTTL,
		instances:  []string{},
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}, nil
}

func (c *Coordinator) Register(ctx context.Context) error {
	if err := c.registerLease(ctx); err != nil {
		return err
	}

	go c.keepAliveLoop()

	log.Printf("Registered instance %s with etcd (lease TTL: %v)", c.instanceID, c.leaseTTL)
	return nil
}

// registerLease grants a fresh lease and writes the instance key under it. It
// does not spawn any goroutines, so it is safe to call again from the
// keepalive loop when the previous lease expires.
func (c *Coordinator) registerLease(ctx context.Context) error {
	ttlSeconds := int64(c.leaseTTL.Seconds())
	if ttlSeconds < 5 {
		ttlSeconds = 5
	}

	leaseResp, err := c.client.Grant(ctx, ttlSeconds)
	if err != nil {
		return fmt.Errorf("failed to create lease: %w", err)
	}

	metadata := InstanceMetadata{
		InstanceID: c.instanceID,
		Region:     c.region,
		StartedAt:  time.Now(),
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	key := c.keyPrefix + c.instanceID
	_, err = c.client.Put(ctx, key, string(metadataJSON), clientv3.WithLease(leaseResp.ID))
	if err != nil {
		return fmt.Errorf("failed to register instance: %w", err)
	}

	c.mu.Lock()
	c.leaseID = leaseResp.ID
	c.mu.Unlock()

	return nil
}

func (c *Coordinator) currentLeaseID() clientv3.LeaseID {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.leaseID
}

// keepAliveLoop keeps the instance lease alive for the lifetime of the
// coordinator. When the keepalive stream dies (etcd restart, network
// partition, lease expiry) it re-registers with backoff and resumes, rather
// than giving up and leaving the process running but invisible to the
// cluster. It is the only goroutine that closes doneCh.
func (c *Coordinator) keepAliveLoop() {
	defer close(c.doneCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-c.stopCh
		cancel()
	}()

	for {
		keepAliveCh, err := c.client.KeepAlive(ctx, c.currentLeaseID())
		if err != nil {
			log.Printf("Failed to start lease keepalive: %v", err)
		} else {
			c.drainKeepAlive(ctx, keepAliveCh)
		}

		select {
		case <-c.stopCh:
			return
		default:
		}

		log.Printf("Lease keepalive lost, re-registering instance %s", c.instanceID)
		if !c.reregisterWithBackoff(ctx) {
			return
		}
	}
}

// drainKeepAlive consumes keepalive responses until the stream closes or the
// coordinator stops.
func (c *Coordinator) drainKeepAlive(ctx context.Context, ch <-chan *clientv3.LeaseKeepAliveResponse) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
		}
	}
}

// reregisterWithBackoff retries registerLease until it succeeds or the
// coordinator stops. Returns false when stopped.
func (c *Coordinator) reregisterWithBackoff(ctx context.Context) bool {
	backoff := time.Second
	for {
		regCtx, regCancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.registerLease(regCtx)
		regCancel()
		if err == nil {
			log.Printf("Re-registered instance %s with a new lease", c.instanceID)
			return true
		}

		log.Printf("Failed to re-register instance %s (retrying in %v): %v", c.instanceID, backoff, err)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Coordinator) Deregister(ctx context.Context) error {
	c.stopOnce.Do(func() { close(c.stopCh) })

	select {
	case <-c.doneCh:
	case <-time.After(5 * time.Second):
		log.Printf("Timeout waiting for keepalive to stop")
	}

	key := c.keyPrefix + c.instanceID
	_, err := c.client.Delete(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to delete instance key: %w", err)
	}

	if leaseID := c.currentLeaseID(); leaseID != 0 {
		_, err = c.client.Revoke(ctx, leaseID)
		if err != nil {
			log.Printf("Failed to revoke lease: %v", err)
		}
	}

	log.Printf("Deregistered instance %s from etcd", c.instanceID)
	return nil
}

func (c *Coordinator) WatchInstances(ctx context.Context, eventCh chan MembershipEvent) error {
	if err := c.refreshInstances(ctx); err != nil {
		return fmt.Errorf("failed to get initial instances: %w", err)
	}

	eventCh <- MembershipEvent{Instances: c.GetInstances()}

	go c.watchLoop(ctx, eventCh)

	return nil
}

func (c *Coordinator) refreshInstances(ctx context.Context) error {
	resp, err := c.client.Get(ctx, c.keyPrefix, clientv3.WithPrefix())
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

func (c *Coordinator) watchLoop(ctx context.Context, eventCh chan MembershipEvent) {
	watchCh := c.client.Watch(ctx, c.keyPrefix, clientv3.WithPrefix())

	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-watchCh:
			if !ok {
				if ctx.Err() != nil {
					return
				}
				// Any membership change that happened while the watch was
				// down was never delivered, so re-list before resuming.
				log.Printf("Watch channel closed, restarting watch")
				watchCh = c.client.Watch(ctx, c.keyPrefix, clientv3.WithPrefix())
				if err := c.refreshInstances(ctx); err != nil {
					log.Printf("Failed to refresh instances after watch restart: %v", err)
					continue
				}
				c.notify(eventCh)
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
				c.notify(eventCh)
			}
		}
	}
}

// notify delivers the current membership to eventCh. Every event carries the
// full instance list, so when the channel is full the oldest pending event is
// discarded in favor of the newest — the consumer always converges on the
// latest membership instead of silently operating on a stale view.
func (c *Coordinator) notify(eventCh chan MembershipEvent) {
	ev := MembershipEvent{Instances: c.GetInstances()}
	for {
		select {
		case eventCh <- ev:
			return
		default:
			select {
			case <-eventCh:
			default:
			}
		}
	}
}

// Reconcile re-lists instances from etcd and returns the fresh membership.
// Callers use it as a periodic backstop so that a missed watch event can never
// leave partition ownership stale for longer than one reconcile interval.
func (c *Coordinator) Reconcile(ctx context.Context) ([]string, error) {
	if err := c.refreshInstances(ctx); err != nil {
		return nil, err
	}
	return c.GetInstances(), nil
}

func (c *Coordinator) InstanceID() string {
	return c.instanceID
}

func (c *Coordinator) GetInstances() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]string, len(c.instances))
	copy(result, c.instances)
	return result
}

func (c *Coordinator) Close() error {
	return c.client.Close()
}
