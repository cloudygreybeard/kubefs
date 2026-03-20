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
	"testing"

	"github.com/jacobsa/fuse/fuseops"
)

func TestInodeIsDir(t *testing.T) {
	tests := []struct {
		nodeType NodeType
		wantDir  bool
	}{
		{NodeRoot, true},
		{NodeNamespace, true},
		{NodeResourceType, true},
		{NodeObject, true},
		{NodeFile, false},
		{NodeDataDir, true},
		{NodeDataFile, false},
	}

	for _, tc := range tests {
		node := &Inode{Type: tc.nodeType}
		if got := node.IsDir(); got != tc.wantDir {
			t.Errorf("Inode{Type: %d}.IsDir() = %v, want %v", tc.nodeType, got, tc.wantDir)
		}
	}
}

func TestInodeMode(t *testing.T) {
	tests := []struct {
		name     string
		node     Inode
		wantMode os.FileMode
	}{
		{"directory", Inode{Type: NodeNamespace}, os.ModeDir | 0o555},
		{"yaml file", Inode{Type: NodeFile, FileName: "yaml"}, 0o644},
		{"json file", Inode{Type: NodeFile, FileName: "json"}, 0o644},
		{"describe file", Inode{Type: NodeFile, FileName: "describe"}, 0o444},
		{"logs file", Inode{Type: NodeFile, FileName: "logs"}, 0o444},
		{"data dir", Inode{Type: NodeDataDir}, os.ModeDir | 0o555},
		{"data file", Inode{Type: NodeDataFile, FileName: "app.conf"}, 0o644},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.node.Mode(); got != tc.wantMode {
				t.Errorf("got %v, want %v", got, tc.wantMode)
			}
		})
	}
}

func TestInodeModeWithVerbs(t *testing.T) {
	tests := []struct {
		name     string
		node     Inode
		writable bool
		wantMode os.FileMode
	}{
		{"yaml writable", Inode{Type: NodeFile, FileName: "yaml"}, true, 0o644},
		{"yaml read-only", Inode{Type: NodeFile, FileName: "yaml"}, false, 0o444},
		{"json writable", Inode{Type: NodeFile, FileName: "json"}, true, 0o644},
		{"json read-only", Inode{Type: NodeFile, FileName: "json"}, false, 0o444},
		{"describe always read-only", Inode{Type: NodeFile, FileName: "describe"}, true, 0o444},
		{"describe read-only mode", Inode{Type: NodeFile, FileName: "describe"}, false, 0o444},
		{"data file writable", Inode{Type: NodeDataFile, FileName: "key"}, true, 0o644},
		{"data file read-only", Inode{Type: NodeDataFile, FileName: "key"}, false, 0o444},
		{"directory unchanged", Inode{Type: NodeNamespace}, false, os.ModeDir | 0o555},
		{"directory writable", Inode{Type: NodeNamespace}, true, os.ModeDir | 0o555},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.node.ModeWithVerbs(tc.writable); got != tc.wantMode {
				t.Errorf("got %o, want %o", got, tc.wantMode)
			}
		})
	}
}

func TestInodeTableRootExists(t *testing.T) {
	it := NewInodeTable()

	root := it.Get(fuseops.RootInodeID)
	if root == nil {
		t.Fatal("root inode should exist")
	}
	if root.Type != NodeRoot {
		t.Errorf("root type = %d, want %d", root.Type, NodeRoot)
	}
}

func TestInodeTableAddAndLookup(t *testing.T) {
	it := NewInodeTable()

	child := it.AddChild(fuseops.RootInodeID, &Inode{
		Name: "default",
		Type: NodeNamespace,
	})

	if child.ID == fuseops.RootInodeID {
		t.Fatal("child should have a different ID than root")
	}
	if child.ParentID != fuseops.RootInodeID {
		t.Errorf("child.ParentID = %d, want %d", child.ParentID, fuseops.RootInodeID)
	}

	found := it.LookupChild(fuseops.RootInodeID, "default")
	if found == nil {
		t.Fatal("LookupChild should find the added child")
	}
	if found.ID != child.ID {
		t.Errorf("found.ID = %d, want %d", found.ID, child.ID)
	}

	missing := it.LookupChild(fuseops.RootInodeID, "nonexistent")
	if missing != nil {
		t.Fatal("LookupChild should return nil for missing child")
	}
}

func TestInodeTableAddDuplicate(t *testing.T) {
	it := NewInodeTable()

	first := it.AddChild(fuseops.RootInodeID, &Inode{
		Name: "ns",
		Type: NodeNamespace,
	})

	second := it.AddChild(fuseops.RootInodeID, &Inode{
		Name: "ns",
		Type: NodeNamespace,
	})

	if first.ID != second.ID {
		t.Errorf("duplicate add should return existing inode: got %d, want %d", second.ID, first.ID)
	}
}

func TestInodeTableChildren(t *testing.T) {
	it := NewInodeTable()

	it.AddChild(fuseops.RootInodeID, &Inode{Name: "a", Type: NodeNamespace})
	it.AddChild(fuseops.RootInodeID, &Inode{Name: "b", Type: NodeNamespace})
	it.AddChild(fuseops.RootInodeID, &Inode{Name: "c", Type: NodeNamespace})

	children := it.Children(fuseops.RootInodeID)
	if len(children) != 3 {
		t.Fatalf("got %d children, want 3", len(children))
	}
}

func TestInodeTableRemoveChildren(t *testing.T) {
	it := NewInodeTable()

	it.AddChild(fuseops.RootInodeID, &Inode{Name: "keep", Type: NodeNamespace})
	it.AddChild(fuseops.RootInodeID, &Inode{Name: "remove", Type: NodeNamespace})

	it.RemoveChildren(fuseops.RootInodeID, map[string]bool{"keep": true})

	children := it.Children(fuseops.RootInodeID)
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	if children[0].Name != "keep" {
		t.Errorf("remaining child name = %q, want %q", children[0].Name, "keep")
	}

	if it.LookupChild(fuseops.RootInodeID, "remove") != nil {
		t.Fatal("removed child should not be findable")
	}
}

func TestInodeTableRemoveChild(t *testing.T) {
	it := NewInodeTable()

	it.AddChild(fuseops.RootInodeID, &Inode{Name: "a", Type: NodeNamespace})
	it.AddChild(fuseops.RootInodeID, &Inode{Name: "b", Type: NodeNamespace})

	if !it.RemoveChild(fuseops.RootInodeID, "a") {
		t.Fatal("RemoveChild should return true for existing child")
	}
	if it.LookupChild(fuseops.RootInodeID, "a") != nil {
		t.Fatal("removed child should not be findable")
	}
	if it.LookupChild(fuseops.RootInodeID, "b") == nil {
		t.Fatal("other child should still exist")
	}

	if it.RemoveChild(fuseops.RootInodeID, "nonexistent") {
		t.Fatal("RemoveChild should return false for missing child")
	}
}
