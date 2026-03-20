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

package fs

import (
	"os"
	"sync"
	"time"

	"github.com/jacobsa/fuse/fuseops"
)

// NodeType classifies inode entries in the virtual filesystem.
type NodeType int

const (
	NodeRoot         NodeType = iota // /
	NodeNamespace                    // /NAMESPACE
	NodeResourceType                 // /NAMESPACE/RESOURCE_TYPE
	NodeObject                       // /NAMESPACE/RESOURCE_TYPE/OBJECT_NAME
	NodeFile                         // /NAMESPACE/RESOURCE_TYPE/OBJECT_NAME/ACTION
	NodeDataDir                      // /NAMESPACE/RESOURCE_TYPE/OBJECT_NAME/data
	NodeDataFile                     // /NAMESPACE/RESOURCE_TYPE/OBJECT_NAME/data/KEY
)

// Inode represents a node in the virtual filesystem tree.
type Inode struct {
	ID       fuseops.InodeID
	Name     string
	Type     NodeType
	ParentID fuseops.InodeID

	// Path components for Kubernetes lookups.
	Namespace    string
	ResourceType string
	ObjectName   string
	FileName     string // "yaml", "json", "describe", "logs"

	// For resource type nodes, the group/version to use with the dynamic client.
	ResourceGroup   string
	ResourceVersion string

	CreationTime time.Time
}

// IsDir returns true if this inode represents a directory.
func (n *Inode) IsDir() bool {
	return n.Type != NodeFile && n.Type != NodeDataFile
}

// Mode returns the file mode for this inode assuming full write access.
// Use ModeWithVerbs when access control is in effect.
func (n *Inode) Mode() os.FileMode {
	if n.IsDir() {
		return os.ModeDir | 0o555
	}
	switch n.Type {
	case NodeDataFile:
		return 0o644
	case NodeFile:
		switch n.FileName {
		case "yaml", "json":
			return 0o644
		default:
			return 0o444
		}
	default:
		return 0o444
	}
}

// ModeWithVerbs returns the file mode adjusted for the given access
// controls. Files that would normally be writable show as 0o444 when
// the update verb is not enabled.
func (n *Inode) ModeWithVerbs(writable bool) os.FileMode {
	m := n.Mode()
	if !writable && m&0o200 != 0 {
		m &^= 0o200
	}
	return m
}

// InodeTable manages dynamic inode allocation and lookup.
// All methods are safe for concurrent use.
type InodeTable struct {
	mu     sync.RWMutex
	inodes map[fuseops.InodeID]*Inode
	// childIndex maps parentID -> childName -> childID for fast lookup.
	childIndex map[fuseops.InodeID]map[string]fuseops.InodeID
	nextID     fuseops.InodeID
}

// NewInodeTable creates an inode table with the root inode pre-allocated.
func NewInodeTable() *InodeTable {
	t := &InodeTable{
		inodes:     make(map[fuseops.InodeID]*Inode),
		childIndex: make(map[fuseops.InodeID]map[string]fuseops.InodeID),
		nextID:     fuseops.RootInodeID + 1,
	}

	root := &Inode{
		ID:           fuseops.RootInodeID,
		Name:         "",
		Type:         NodeRoot,
		ParentID:     fuseops.RootInodeID,
		CreationTime: time.Now(),
	}
	t.inodes[fuseops.RootInodeID] = root
	t.childIndex[fuseops.RootInodeID] = make(map[string]fuseops.InodeID)

	return t
}

// Get returns an inode by ID, or nil if not found.
func (t *InodeTable) Get(id fuseops.InodeID) *Inode {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.inodes[id]
}

// LookupChild returns the child inode of the given parent with the given name,
// or nil if no such child exists.
func (t *InodeTable) LookupChild(parentID fuseops.InodeID, name string) *Inode {
	t.mu.RLock()
	defer t.mu.RUnlock()
	children, ok := t.childIndex[parentID]
	if !ok {
		return nil
	}
	childID, ok := children[name]
	if !ok {
		return nil
	}
	return t.inodes[childID]
}

// Children returns all child inode IDs for the given parent.
func (t *InodeTable) Children(parentID fuseops.InodeID) []*Inode {
	t.mu.RLock()
	defer t.mu.RUnlock()
	children, ok := t.childIndex[parentID]
	if !ok {
		return nil
	}
	result := make([]*Inode, 0, len(children))
	for _, childID := range children {
		if n := t.inodes[childID]; n != nil {
			result = append(result, n)
		}
	}
	return result
}

// AddChild adds a new inode as a child of the given parent. If a child with
// the same name already exists, it returns the existing inode without
// modification.
func (t *InodeTable) AddChild(parentID fuseops.InodeID, child *Inode) *Inode {
	t.mu.Lock()
	defer t.mu.Unlock()

	children, ok := t.childIndex[parentID]
	if !ok {
		children = make(map[string]fuseops.InodeID)
		t.childIndex[parentID] = children
	}

	if existingID, ok := children[child.Name]; ok {
		return t.inodes[existingID]
	}

	child.ID = t.nextID
	child.ParentID = parentID
	t.nextID++

	t.inodes[child.ID] = child
	children[child.Name] = child.ID

	if child.IsDir() {
		t.childIndex[child.ID] = make(map[string]fuseops.InodeID)
	}

	return child
}

// RemoveChild removes a single child by name from the given parent. Returns
// true if the child was found and removed.
func (t *InodeTable) RemoveChild(parentID fuseops.InodeID, name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	children, ok := t.childIndex[parentID]
	if !ok {
		return false
	}
	childID, ok := children[name]
	if !ok {
		return false
	}
	t.removeSubtree(childID)
	delete(children, name)
	return true
}

// RemoveChildren removes all children of the given parent that are not in the
// keep set (by name). This supports re-syncing directory contents when the
// backing list changes.
func (t *InodeTable) RemoveChildren(parentID fuseops.InodeID, keep map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	children, ok := t.childIndex[parentID]
	if !ok {
		return
	}
	for name, childID := range children {
		if !keep[name] {
			t.removeSubtree(childID)
			delete(children, name)
		}
	}
}

// removeSubtree recursively removes an inode and all descendants.
// Must be called with t.mu held.
func (t *InodeTable) removeSubtree(id fuseops.InodeID) {
	if children, ok := t.childIndex[id]; ok {
		for _, childID := range children {
			t.removeSubtree(childID)
		}
		delete(t.childIndex, id)
	}
	delete(t.inodes, id)
}
