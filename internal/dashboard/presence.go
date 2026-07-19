package dashboard

import (
	"sync"
	"time"
)

const presenceHeartbeatTTL = 60 * time.Second

// ViewerInfo tracks a user currently viewing a resource.
type ViewerInfo struct {
	UserID   string
	Name     string
	Email    string
	Picture  string
	LastSeen time.Time
}

// PresenceTracker tracks which users are currently viewing resources.
// Uses an in-memory map with heartbeat-based TTL eviction.
type PresenceTracker struct {
	mu      sync.RWMutex
	viewers map[string]map[string]*ViewerInfo // resourceKey → userID → ViewerInfo
}

// NewPresenceTracker creates a new PresenceTracker and starts the eviction goroutine.
func NewPresenceTracker() *PresenceTracker {
	pt := &PresenceTracker{
		viewers: make(map[string]map[string]*ViewerInfo),
	}
	go pt.evictLoop()
	return pt
}

func resourceKey(resourceType, resourceID, resourceNS string) string {
	return resourceType + "/" + resourceNS + "/" + resourceID
}

// Heartbeat records or refreshes a user's presence on a resource.
func (pt *PresenceTracker) Heartbeat(resourceType, resourceID, resourceNS string, viewer ViewerInfo) {
	key := resourceKey(resourceType, resourceID, resourceNS)
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if pt.viewers[key] == nil {
		pt.viewers[key] = make(map[string]*ViewerInfo)
	}
	viewer.LastSeen = time.Now()
	pt.viewers[key][viewer.UserID] = &viewer
}

// GetViewers returns the list of active viewers for a resource.
func (pt *PresenceTracker) GetViewers(resourceType, resourceID, resourceNS string) []ViewerInfo {
	key := resourceKey(resourceType, resourceID, resourceNS)
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	cutoff := time.Now().Add(-presenceHeartbeatTTL)
	var result []ViewerInfo
	for _, v := range pt.viewers[key] {
		if v.LastSeen.After(cutoff) {
			result = append(result, *v)
		}
	}
	return result
}

// evictLoop periodically removes stale entries.
func (pt *PresenceTracker) evictLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		pt.evictStale()
	}
}

func (pt *PresenceTracker) evictStale() {
	cutoff := time.Now().Add(-presenceHeartbeatTTL)
	pt.mu.Lock()
	defer pt.mu.Unlock()
	for key, users := range pt.viewers {
		for uid, v := range users {
			if v.LastSeen.Before(cutoff) {
				delete(users, uid)
			}
		}
		if len(users) == 0 {
			delete(pt.viewers, key)
		}
	}
}
