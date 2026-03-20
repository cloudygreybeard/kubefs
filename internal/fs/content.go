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
	"context"
	"fmt"

	"github.com/cloudygreybeard/kubefs/internal/k8s"
)

// ContentProvider fetches virtual file content from the Kubernetes API.
type ContentProvider struct {
	client *k8s.Client
}

// NewContentProvider creates a ContentProvider backed by the given client.
func NewContentProvider(client *k8s.Client) *ContentProvider {
	return &ContentProvider{client: client}
}

// GetContent returns the content of a virtual file identified by the inode.
func (p *ContentProvider) GetContent(ctx context.Context, node *Inode) ([]byte, error) {
	ri := k8s.ResourceInfo{
		Group:    node.ResourceGroup,
		Version:  node.ResourceVersion,
		Resource: node.ResourceType,
	}

	switch node.Type {
	case NodeDataFile:
		dataMap, err := p.client.GetObjectData(ctx, node.Namespace, ri, node.ObjectName)
		if err != nil {
			return nil, err
		}
		v, ok := dataMap[node.FileName]
		if !ok {
			return nil, fmt.Errorf("key %q not found in %s/%s", node.FileName, node.ResourceType, node.ObjectName)
		}
		return v, nil

	case NodeFile:
		switch node.FileName {
		case "yaml":
			return p.client.GetObjectYAML(ctx, node.Namespace, node.ResourceType, node.ObjectName, ri)
		case "json":
			return p.client.GetObjectJSON(ctx, node.Namespace, node.ResourceType, node.ObjectName, ri)
		case "describe":
			return p.client.DescribeObject(ctx, node.Namespace, node.ResourceType, node.ObjectName, ri)
		case "logs":
			return p.client.GetPodLogs(ctx, node.Namespace, node.ObjectName)
		default:
			return nil, fmt.Errorf("unknown file type: %s", node.FileName)
		}

	default:
		return nil, fmt.Errorf("not a file node")
	}
}

// CreateContent creates a new Kubernetes object from the file content.
func (p *ContentProvider) CreateContent(ctx context.Context, node *Inode, data []byte) error {
	ri := k8s.ResourceInfo{
		Group:    node.ResourceGroup,
		Version:  node.ResourceVersion,
		Resource: node.ResourceType,
	}
	return p.client.CreateObject(ctx, node.Namespace, ri, data)
}

// ApplyContent writes modified content back to the Kubernetes API.
func (p *ContentProvider) ApplyContent(ctx context.Context, node *Inode, data []byte) error {
	ri := k8s.ResourceInfo{
		Group:    node.ResourceGroup,
		Version:  node.ResourceVersion,
		Resource: node.ResourceType,
	}

	switch node.Type {
	case NodeDataFile:
		return p.client.UpdateObjectDataKey(ctx, node.Namespace, ri, node.ObjectName, node.FileName, data)

	case NodeFile:
		switch node.FileName {
		case "yaml", "json":
			return p.client.ApplyObject(ctx, node.Namespace, ri, data)
		default:
			return fmt.Errorf("file %s is read-only", node.FileName)
		}

	default:
		return fmt.Errorf("not a file node")
	}
}

// ActionFiles returns the virtual file names available for an object.
// Pods get an additional "logs" file.
func ActionFiles(resourceType string) []string {
	files := []string{"yaml", "json", "describe"}
	if resourceType == "pods" {
		files = append(files, "logs")
	}
	return files
}

// HasDataDir returns true if the resource type exposes a data/ subdirectory.
func HasDataDir(resourceType string) bool {
	return resourceType == "configmaps" || resourceType == "secrets"
}
