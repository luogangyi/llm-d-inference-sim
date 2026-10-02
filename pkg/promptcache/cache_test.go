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

package promptcache

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestPrefixResolution(t *testing.T) {
	cache := New(Options{MaxEntries: 10, MaxTotalTokens: 100, TTL: time.Minute})
	if got := cache.LookupAndStore("model-a", []uint32{1, 2, 3, 4}); got != 0 {
		t.Fatalf("first request hit = %d, want 0", got)
	}
	for _, tc := range []struct {
		name      string
		namespace string
		tokens    []uint32
		want      int
	}{
		{"identical", "model-a", []uint32{1, 2, 3, 4}, 4},
		{"extension", "model-a", []uint32{1, 2, 3, 4, 5}, 4},
		{"partial", "model-a", []uint32{1, 2, 9}, 2},
		{"different model", "model-b", []uint32{1, 2, 3, 4}, 0},
		{"different first token", "model-a", []uint32{9, 2, 3}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cache.LookupAndStore(tc.namespace, tc.tokens); got != tc.want {
				t.Fatalf("hit = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestConcurrentWorkloadsAtCapacity(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		concurrency, prompt, cached int
	}{
		{"100x100k", 100, 100000, 80000},
		{"1000x10k", 1000, 10000, 8000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := New(Options{MaxEntries: 2 * tc.concurrency, MaxTotalTokens: 10000000, TTL: 5 * time.Minute})
			for i := range tc.concurrency {
				prefix := make([]uint32, tc.cached)
				prefix[0] = uint32(i + 1)
				if got := cache.LookupAndStore("load", prefix); got != 0 {
					t.Fatalf("warm-up hit = %d", got)
				}
			}
			var wg sync.WaitGroup
			errors := make(chan int, tc.concurrency)
			start := make(chan struct{})
			for i := range tc.concurrency {
				wg.Add(1)
				go func() {
					defer wg.Done()
					prompt := make([]uint32, tc.prompt)
					prompt[0] = uint32(i + 1)
					prompt[tc.cached] = uint32(i + 10001)
					<-start
					if got := cache.LookupAndStore("load", prompt); got != tc.cached {
						errors <- got
					}
				}()
			}
			begin := time.Now()
			close(start)
			wg.Wait()
			close(errors)
			for got := range errors {
				t.Errorf("hit = %d, want %d", got, tc.cached)
			}
			if stats := cache.Stats(); stats.StoredTokens > 10000000 || stats.Entries != 2*tc.concurrency {
				t.Fatalf("capacity exceeded or entry lost: %+v", stats)
			}
			runtime.GC()
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			runtime.KeepAlive(cache)
			t.Logf("concurrency=%d context=%d elapsed=%s heap_alloc=%d", tc.concurrency, tc.prompt, time.Since(begin), mem.HeapAlloc)
		})
	}
}

func TestCapacityTTLAndInputOwnership(t *testing.T) {
	cache := New(Options{MaxEntries: 2, MaxTotalTokens: 6, TTL: time.Minute})
	now := time.Now()
	cache.now = func() time.Time { return now }
	first := []uint32{1, 2, 3}
	cache.LookupAndStore("a", first)
	first[0] = 99
	cache.LookupAndStore("a", []uint32{4, 5, 6})
	if got := cache.LookupAndStore("a", []uint32{1, 2, 3}); got != 3 {
		t.Fatalf("cached input was mutated, hit = %d", got)
	}
	cache.LookupAndStore("a", []uint32{7, 8, 9})
	if got := cache.LookupAndStore("a", []uint32{4, 5, 6}); got != 0 {
		t.Fatalf("LRU entry was not evicted, hit = %d", got)
	}
	now = now.Add(time.Minute + time.Second)
	if got := cache.LookupAndStore("a", []uint32{7, 8, 9}); got != 0 {
		t.Fatalf("expired entry hit = %d", got)
	}
}

func TestSharedPrefixAndOversizedPrompt(t *testing.T) {
	cache := New(Options{MaxEntries: 2, MaxTotalTokens: 5, TTL: time.Minute})
	cache.LookupAndStore("a", []uint32{1, 2, 3, 4})
	cache.LookupAndStore("a", []uint32{1, 2, 3, 5})
	if got := cache.StoredTokens(); got != 5 {
		t.Fatalf("shared prefix counted twice: %d", got)
	}
	if got := cache.LookupAndStore("a", []uint32{9, 9, 9, 9, 9, 9}); got != 0 {
		t.Fatalf("oversized prompt hit = %d", got)
	}
	if got := cache.StoredTokens(); got != 5 {
		t.Fatalf("oversized prompt changed cache size: %d", got)
	}
}

func TestConcurrentResolution(t *testing.T) {
	cache := New(Options{MaxEntries: 200, MaxTotalTokens: 2000, TTL: time.Minute})
	var wg sync.WaitGroup
	for worker := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				cache.LookupAndStore("a", []uint32{1, 2, uint32(worker), uint32(i)})
			}
		}()
	}
	wg.Wait()
	if cache.StoredTokens() > 2000 {
		t.Fatalf("cache exceeded token limit: %d", cache.StoredTokens())
	}
}

func TestResolutionStatsAndClear(t *testing.T) {
	cache := New(Options{MaxEntries: 1, MaxTotalTokens: 4, TTL: time.Minute})
	first := cache.Resolve("a", []uint32{1, 2, 3})
	if first.HitTokens != 0 || first.WrittenTokens != 3 {
		t.Fatalf("first resolution = %+v", first)
	}
	second := cache.Resolve("a", []uint32{1, 2, 3})
	if second.HitTokens != 3 || second.WrittenTokens != 0 {
		t.Fatalf("repeat resolution = %+v", second)
	}
	oversized := cache.Resolve("a", []uint32{9, 8, 7, 6, 5})
	if oversized.WrittenTokens != 0 {
		t.Fatalf("oversized resolution wrote tokens: %+v", oversized)
	}
	stats := cache.Stats()
	if stats.Entries != 1 || stats.StoredTokens != 3 || stats.Requests != 3 || stats.HitTokens != 3 {
		t.Fatalf("stats = %+v", stats)
	}
	cache.Clear()
	if stats := cache.Stats(); stats.Entries != 0 || stats.StoredTokens != 0 || stats.Namespaces != 0 {
		t.Fatalf("clear left entries: %+v", stats)
	}
}

func TestMinimumReportedPrefix(t *testing.T) {
	cache := New(Options{MaxEntries: 10, MaxTotalTokens: 100, MinPrefixTokens: 3, TTL: time.Minute})
	cache.Resolve("a", []uint32{1, 2, 3})
	partial := cache.Resolve("a", []uint32{1, 2, 9})
	if partial.HitTokens != 0 || partial.WrittenTokens != 1 {
		t.Fatalf("below-threshold resolution = %+v", partial)
	}
	if got := cache.Resolve("a", []uint32{1, 2, 9}); got.HitTokens != 3 {
		t.Fatalf("full prefix must still be stored: %+v", got)
	}
}

func TestClearMatchingNamespace(t *testing.T) {
	cache := New(Options{MaxEntries: 10, MaxTotalTokens: 100, TTL: time.Minute})
	cache.Resolve("model-a", []uint32{1, 2, 3})
	cache.Resolve("model-b", []uint32{1, 2, 3})
	if removed := cache.ClearMatching(func(namespace string) bool { return namespace == "model-a" }); removed != 1 {
		t.Fatalf("removed = %d", removed)
	}
	if got := cache.Resolve("model-a", []uint32{1, 2, 3}).HitTokens; got != 0 {
		t.Fatalf("cleared namespace hit = %d", got)
	}
	if got := cache.Resolve("model-b", []uint32{1, 2, 3}).HitTokens; got != 3 {
		t.Fatalf("retained namespace hit = %d", got)
	}
}
