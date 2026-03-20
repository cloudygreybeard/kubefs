// Copyright 2026 cloudygreybeard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package k8s

import (
	"testing"
	"time"
)

func TestCacheGetSet(t *testing.T) {
	c := NewCache(time.Minute)

	if _, ok := c.Get("missing"); ok {
		t.Fatal("expected miss for absent key")
	}

	c.Set("key", "value")
	v, ok := c.Get("key")
	if !ok {
		t.Fatal("expected hit after Set")
	}
	if v.(string) != "value" {
		t.Fatalf("got %q, want %q", v, "value")
	}
}

func TestCacheExpiry(t *testing.T) {
	c := NewCache(10 * time.Millisecond)

	c.Set("k", 42)
	if _, ok := c.Get("k"); !ok {
		t.Fatal("expected hit before expiry")
	}

	time.Sleep(20 * time.Millisecond)

	if _, ok := c.Get("k"); ok {
		t.Fatal("expected miss after expiry")
	}
}

func TestCacheDelete(t *testing.T) {
	c := NewCache(time.Minute)

	c.Set("a", 1)
	c.Set("b", 2)

	c.Delete("a")

	if _, ok := c.Get("a"); ok {
		t.Fatal("expected miss after Delete")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("expected hit for non-deleted key")
	}
}

func TestCacheDeletePrefix(t *testing.T) {
	c := NewCache(time.Minute)

	c.Set("ns:pods:a", 1)
	c.Set("ns:pods:b", 2)
	c.Set("ns:services:c", 3)
	c.Set("other:pods:d", 4)

	c.DeletePrefix("ns:pods:")

	if _, ok := c.Get("ns:pods:a"); ok {
		t.Fatal("expected miss for prefixed key a")
	}
	if _, ok := c.Get("ns:pods:b"); ok {
		t.Fatal("expected miss for prefixed key b")
	}
	if _, ok := c.Get("ns:services:c"); !ok {
		t.Fatal("expected hit for non-matching prefix")
	}
	if _, ok := c.Get("other:pods:d"); !ok {
		t.Fatal("expected hit for different prefix")
	}
}

func TestCacheOverwrite(t *testing.T) {
	c := NewCache(time.Minute)

	c.Set("k", "first")
	c.Set("k", "second")

	v, ok := c.Get("k")
	if !ok {
		t.Fatal("expected hit")
	}
	if v.(string) != "second" {
		t.Fatalf("got %q, want %q", v, "second")
	}
}
