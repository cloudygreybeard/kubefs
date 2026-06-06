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

package kubefs

import (
	"os"
	"sync"
	"time"
)

// InodeID is an opaque filesystem node identifier.
type InodeID = uint64

// RootInodeID is the well-known inode number for the filesystem root.
const RootInodeID InodeID = 1

// HandleID identifies an open file handle.
type HandleID = uint64

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
	ID       InodeID
	Name     string
	Type     NodeType
	ParentID InodeID

	Namespace    string
	ResourceType string
	ObjectName   string
	FileName     string

	ResourceGroup   string
	ResourceVersion string

	CreationTime time.Time
}

// IsDir returns true if this inode represents a directory.
func (n *Inode) IsDir() bool {
	return n.Type != NodeFile && n.Type != NodeDataFile
}

// Mode returns the file mode for this inode assuming full write access.
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

// ModeWithVerbs returns the file mode adjusted for access controls.
// Writable files show as 0o444 when the update verb is not enabled.
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
	mu         sync.RWMutex
	inodes     map[InodeID]*Inode
	childIndex map[InodeID]map[string]InodeID
	nextID     InodeID
}

// NewInodeTable creates an inode table with the root inode pre-allocated.
func NewInodeTable() *InodeTable {
	t := &InodeTable{
		inodes:     make(map[InodeID]*Inode),
		childIndex: make(map[InodeID]map[string]InodeID),
		nextID:     RootInodeID + 1,
	}

	root := &Inode{
		ID:           RootInodeID,
		Name:         "",
		Type:         NodeRoot,
		ParentID:     RootInodeID,
		CreationTime: time.Now(),
	}
	t.inodes[RootInodeID] = root
	t.childIndex[RootInodeID] = make(map[string]InodeID)

	return t
}

// Get returns an inode by ID, or nil if not found.
func (t *InodeTable) Get(id InodeID) *Inode {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.inodes[id]
}

// LookupChild returns the child of the given parent with the given name.
func (t *InodeTable) LookupChild(parentID InodeID, name string) *Inode {
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

// Children returns all child inodes for the given parent.
func (t *InodeTable) Children(parentID InodeID) []*Inode {
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

// AddChild adds a new inode as a child of the given parent. If a child
// with the same name already exists, it returns the existing inode.
func (t *InodeTable) AddChild(parentID InodeID, child *Inode) *Inode {
	t.mu.Lock()
	defer t.mu.Unlock()

	children, ok := t.childIndex[parentID]
	if !ok {
		children = make(map[string]InodeID)
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
		t.childIndex[child.ID] = make(map[string]InodeID)
	}

	return child
}

// RemoveChild removes a single child by name from the given parent.
func (t *InodeTable) RemoveChild(parentID InodeID, name string) bool {
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

// RemoveChildren removes all children not in the keep set.
func (t *InodeTable) RemoveChildren(parentID InodeID, keep map[string]bool) {
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

func (t *InodeTable) removeSubtree(id InodeID) {
	if children, ok := t.childIndex[id]; ok {
		for _, childID := range children {
			t.removeSubtree(childID)
		}
		delete(t.childIndex, id)
	}
	delete(t.inodes, id)
}
