/*
Copyright 2026 The llm-d-inference-sim Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package promptcache records tokenized prompts in a bounded in-memory prefix trie.
package promptcache

import (
	"container/list"
	"sync"
	"time"
)

type Options struct {
	MaxEntries      int
	MaxTotalTokens  int
	MinPrefixTokens int
	TTL             time.Duration
}

type Resolution struct {
	HitTokens         int
	WrittenTokens     int
	TTLEvictions      int
	CapacityEvictions int
	Entries           int
	StoredTokens      int
}

type Statistics struct {
	Namespaces        int    `json:"namespaces"`
	Entries           int    `json:"entries"`
	StoredTokens      int    `json:"tokens"`
	Requests          uint64 `json:"requests"`
	QueriedTokens     uint64 `json:"queried_tokens"`
	HitTokens         uint64 `json:"hit_tokens"`
	WrittenTokens     uint64 `json:"written_tokens"`
	TTLEvictions      uint64 `json:"ttl_evictions"`
	CapacityEvictions uint64 `json:"capacity_evictions"`
}

type Snapshot struct {
	Statistics
	Source  string  `json:"source"`
	Epoch   uint64  `json:"epoch"`
	HitRate float64 `json:"hit_rate"`
}

type entry struct {
	node       *node
	namespace  string
	created    time.Time
	oldest     *list.Element
	mostRecent *list.Element
}

type node struct {
	edge     []uint32
	parent   *node
	children map[uint32]*node
	entry    *entry
}

type Cache struct {
	mu                sync.Mutex
	options           Options
	serviceRoots      map[string]*node
	oldest            list.List
	lru               list.List
	storedTokens      int
	entries           int
	requests          uint64
	queried           uint64
	hits              uint64
	written           uint64
	ttlEvictions      uint64
	capacityEvictions uint64
	now               func() time.Time
}

func New(options Options) *Cache {
	return &Cache{
		options:      options,
		serviceRoots: make(map[string]*node),
		now:          time.Now,
	}
}

// LookupAndStore returns the longest shared token prefix and records the full
// prompt for subsequent requests. The cache owns a copy of the prompt tokens.
func (c *Cache) LookupAndStore(namespace string, tokens []uint32) int {
	return c.Resolve(namespace, tokens).HitTokens
}

func (c *Cache) Resolve(namespace string, tokens []uint32) Resolution {
	if len(tokens) == 0 {
		return Resolution{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	result := Resolution{}
	result.TTLEvictions = c.expire(now)
	root := c.serviceRoots[namespace]
	if root == nil {
		root = &node{children: make(map[uint32]*node)}
		c.serviceRoots[namespace] = root
	}
	rawHit, found := longestPrefix(root, tokens)
	result.HitTokens = rawHit
	if rawHit < c.options.MinPrefixTokens {
		result.HitTokens = 0
	}
	c.requests++
	c.queried += uint64(len(tokens))
	c.hits += uint64(result.HitTokens)
	if rawHit > 0 && found != nil {
		if e := firstEntry(found); e != nil {
			c.lru.MoveToFront(e.mostRecent)
		}
	}
	if c.options.MaxEntries <= 0 || c.options.MaxTotalTokens <= 0 || len(tokens) > c.options.MaxTotalTokens {
		result.Entries, result.StoredTokens = c.entries, c.storedTokens
		return result
	}
	terminal := c.insert(root, tokens)
	if terminal.entry == nil {
		result.WrittenTokens = len(tokens) - rawHit
		c.written += uint64(result.WrittenTokens)
		e := &entry{node: terminal, namespace: namespace, created: now}
		e.oldest = c.oldest.PushBack(e)
		e.mostRecent = c.lru.PushFront(e)
		terminal.entry = e
		c.entries++
	}
	for c.entries > c.options.MaxEntries || c.storedTokens > c.options.MaxTotalTokens {
		c.remove(c.lru.Back().Value.(*entry))
		c.capacityEvictions++
		result.CapacityEvictions++
	}
	result.Entries, result.StoredTokens = c.entries, c.storedTokens
	return result
}

func longestPrefix(root *node, tokens []uint32) (int, *node) {
	current := root
	hit := 0
	for hit < len(tokens) {
		child := current.children[tokens[hit]]
		if child == nil {
			return hit, current
		}
		shared := commonPrefix(child.edge, tokens[hit:])
		hit += shared
		if shared < len(child.edge) {
			return hit, child
		}
		current = child
	}
	return hit, current
}

func firstEntry(n *node) *entry {
	if n.entry != nil {
		return n.entry
	}
	for _, child := range n.children {
		if e := firstEntry(child); e != nil {
			return e
		}
	}
	return nil
}

func commonPrefix(a, b []uint32) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

func (c *Cache) insert(root *node, tokens []uint32) *node {
	current := root
	remaining := tokens
	for len(remaining) > 0 {
		child := current.children[remaining[0]]
		if child == nil {
			child = &node{edge: append([]uint32(nil), remaining...), parent: current, children: make(map[uint32]*node)}
			current.children[remaining[0]] = child
			c.storedTokens += len(remaining)
			return child
		}
		shared := commonPrefix(child.edge, remaining)
		if shared < len(child.edge) {
			branch := &node{
				edge: append([]uint32(nil), child.edge[:shared]...), parent: current,
				children: make(map[uint32]*node),
			}
			child.edge = append([]uint32(nil), child.edge[shared:]...)
			child.parent = branch
			branch.children[child.edge[0]] = child
			current.children[branch.edge[0]] = branch
			current = branch
			remaining = remaining[shared:]
			if len(remaining) == 0 {
				return branch
			}
			continue
		}
		current = child
		remaining = remaining[shared:]
	}
	return current
}

func (c *Cache) expire(now time.Time) int {
	removed := 0
	for oldest := c.oldest.Front(); oldest != nil; oldest = c.oldest.Front() {
		e := oldest.Value.(*entry)
		if now.Sub(e.created) < c.options.TTL {
			return removed
		}
		c.remove(e)
		removed++
		c.ttlEvictions++
	}
	return removed
}

func (c *Cache) remove(e *entry) {
	n := e.node
	n.entry = nil
	c.oldest.Remove(e.oldest)
	c.lru.Remove(e.mostRecent)
	c.entries--
	for n.parent != nil && n.entry == nil && len(n.children) == 0 {
		parent := n.parent
		delete(parent.children, n.edge[0])
		c.storedTokens -= len(n.edge)
		n = parent
	}
	if n.parent == nil && len(n.children) == 0 {
		delete(c.serviceRoots, e.namespace)
	}
}

func (c *Cache) StoredTokens() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.storedTokens
}

func (c *Cache) Stats() Statistics {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire(c.now())
	return Statistics{
		Namespaces: len(c.serviceRoots), Entries: c.entries, StoredTokens: c.storedTokens,
		Requests: c.requests, QueriedTokens: c.queried, HitTokens: c.hits,
		WrittenTokens: c.written, TTLEvictions: c.ttlEvictions, CapacityEvictions: c.capacityEvictions,
	}
}

func (c *Cache) Clear() int {
	return c.ClearMatching(nil)
}

func (c *Cache) ClearMatching(match func(string) bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if match != nil {
		removed := 0
		for item := c.oldest.Front(); item != nil; {
			next := item.Next()
			e := item.Value.(*entry)
			if match(e.namespace) {
				c.remove(e)
				removed++
			}
			item = next
		}
		return removed
	}
	removed := c.entries
	c.serviceRoots = make(map[string]*node)
	c.oldest.Init()
	c.lru.Init()
	c.entries = 0
	c.storedTokens = 0
	return removed
}
