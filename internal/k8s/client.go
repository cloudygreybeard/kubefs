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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	sigsyaml "sigs.k8s.io/yaml"
)

// ResourceInfo holds metadata about a discovered API resource.
type ResourceInfo struct {
	Group    string
	Version  string
	Resource string
	Kind     string
}

// GVR returns the GroupVersionResource for this resource.
func (r ResourceInfo) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    r.Group,
		Version:  r.Version,
		Resource: r.Resource,
	}
}

// ObjectInfo holds basic metadata about a Kubernetes object.
type ObjectInfo struct {
	Name              string
	CreationTimestamp time.Time
}

// Client wraps Kubernetes API access with caching.
type Client struct {
	clientset *kubernetes.Clientset
	dynamic   dynamic.Interface
	discovery discovery.DiscoveryInterface
	cache     *Cache
}

// NewClient creates a Client from a kubeconfig path and optional context name.
func NewClient(kubeconfig, kubecontext string, cacheTTL time.Duration) (*Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}

	overrides := &clientcmd.ConfigOverrides{}
	if kubecontext != "" {
		overrides.CurrentContext = kubecontext
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building kubeconfig: %w", err)
	}

	return NewClientFromConfig(config, cacheTTL)
}

// NewClientFromConfig creates a Client from a rest.Config.
func NewClientFromConfig(config *rest.Config, cacheTTL time.Duration) (*Client, error) {
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating clientset: %w", err)
	}

	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	return &Client{
		clientset: cs,
		dynamic:   dyn,
		discovery: cs.Discovery(),
		cache:     NewCache(cacheTTL),
	}, nil
}

// ListNamespaces returns all namespace names.
func (c *Client) ListNamespaces(ctx context.Context) ([]string, error) {
	if v, ok := c.cache.Get("namespaces"); ok {
		return v.([]string), nil
	}

	list, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}

	names := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		names = append(names, ns.Name)
	}
	sort.Strings(names)

	c.cache.Set("namespaces", names)
	return names, nil
}

// DiscoverResources returns the namespaced API resources available on the server.
// The returned map is keyed by the plural resource name (e.g. "pods", "deployments").
func (c *Client) DiscoverResources(ctx context.Context) (map[string]ResourceInfo, error) {
	if v, ok := c.cache.Get("resources"); ok {
		return v.(map[string]ResourceInfo), nil
	}

	lists, err := c.discovery.ServerPreferredNamespacedResources()
	if err != nil {
		// Discovery can return partial results alongside errors for
		// unavailable API groups. Use what we got if there are any.
		if len(lists) == 0 {
			return nil, fmt.Errorf("discovering resources: %w", err)
		}
	}

	resources := make(map[string]ResourceInfo)
	for _, list := range lists {
		gv, parseErr := schema.ParseGroupVersion(list.GroupVersion)
		if parseErr != nil {
			continue
		}
		for _, r := range list.APIResources {
			if !containsVerb(r.Verbs, "list") || !containsVerb(r.Verbs, "get") {
				continue
			}
			// Skip sub-resources (e.g. "pods/log").
			if strings.Contains(r.Name, "/") {
				continue
			}
			// Prefer the first discovery hit (server-preferred version).
			if _, exists := resources[r.Name]; !exists {
				resources[r.Name] = ResourceInfo{
					Group:    gv.Group,
					Version:  gv.Version,
					Resource: r.Name,
					Kind:     r.Kind,
				}
			}
		}
	}

	c.cache.Set("resources", resources)
	return resources, nil
}

// ListObjects returns object names for a resource in a namespace.
func (c *Client) ListObjects(ctx context.Context, ns string, ri ResourceInfo) ([]ObjectInfo, error) {
	key := fmt.Sprintf("list:%s:%s", ns, ri.Resource)
	if v, ok := c.cache.Get(key); ok {
		return v.([]ObjectInfo), nil
	}

	list, err := c.dynamic.Resource(ri.GVR()).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing %s in %s: %w", ri.Resource, ns, err)
	}

	objects := make([]ObjectInfo, 0, len(list.Items))
	for _, item := range list.Items {
		objects = append(objects, ObjectInfo{
			Name:              item.GetName(),
			CreationTimestamp: item.GetCreationTimestamp().Time,
		})
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Name < objects[j].Name })

	c.cache.Set(key, objects)
	return objects, nil
}

// GetObjectYAML returns the YAML representation of an object.
func (c *Client) GetObjectYAML(ctx context.Context, ns, resource, name string, ri ResourceInfo) ([]byte, error) {
	key := fmt.Sprintf("yaml:%s:%s:%s", ns, resource, name)
	if v, ok := c.cache.Get(key); ok {
		return v.([]byte), nil
	}

	obj, err := c.dynamic.Resource(ri.GVR()).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting %s/%s in %s: %w", resource, name, ns, err)
	}

	data, err := sigsyaml.Marshal(obj.Object)
	if err != nil {
		return nil, fmt.Errorf("marshalling yaml: %w", err)
	}

	c.cache.Set(key, data)
	return data, nil
}

// GetObjectJSON returns the JSON representation of an object.
func (c *Client) GetObjectJSON(ctx context.Context, ns, resource, name string, ri ResourceInfo) ([]byte, error) {
	key := fmt.Sprintf("json:%s:%s:%s", ns, resource, name)
	if v, ok := c.cache.Get(key); ok {
		return v.([]byte), nil
	}

	obj, err := c.dynamic.Resource(ri.GVR()).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting %s/%s in %s: %w", resource, name, ns, err)
	}

	data, err := json.MarshalIndent(obj.Object, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling json: %w", err)
	}
	data = append(data, '\n')

	c.cache.Set(key, data)
	return data, nil
}

// DescribeObject returns a human-readable description of an object,
// similar to `kubectl describe`.
func (c *Client) DescribeObject(ctx context.Context, ns, resource, name string, ri ResourceInfo) ([]byte, error) {
	key := fmt.Sprintf("describe:%s:%s:%s", ns, resource, name)
	if v, ok := c.cache.Get(key); ok {
		return v.([]byte), nil
	}

	obj, err := c.dynamic.Resource(ri.GVR()).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting %s/%s in %s: %w", resource, name, ns, err)
	}

	data := formatDescription(obj, ri)

	c.cache.Set(key, data)
	return data, nil
}

// GetPodLogs returns the logs for a pod.
func (c *Client) GetPodLogs(ctx context.Context, ns, name string) ([]byte, error) {
	key := fmt.Sprintf("logs:%s:%s", ns, name)
	if v, ok := c.cache.Get(key); ok {
		return v.([]byte), nil
	}

	req := c.clientset.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{})
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting logs for %s in %s: %w", name, ns, err)
	}
	defer func() { _ = stream.Close() }()

	data, err := io.ReadAll(stream)
	if err != nil {
		return nil, fmt.Errorf("reading logs: %w", err)
	}

	c.cache.Set(key, data)
	return data, nil
}

// CreateObject creates a new Kubernetes object from raw JSON or YAML data.
func (c *Client) CreateObject(ctx context.Context, ns string, ri ResourceInfo, data []byte) error {
	jsonData := data
	if !json.Valid(data) {
		var err error
		jsonData, err = sigsyaml.YAMLToJSON(data)
		if err != nil {
			return fmt.Errorf("converting yaml to json: %w", err)
		}
	}

	var obj unstructured.Unstructured
	if err := json.Unmarshal(jsonData, &obj.Object); err != nil {
		return fmt.Errorf("unmarshalling object: %w", err)
	}

	_, err := c.dynamic.Resource(ri.GVR()).Namespace(ns).Create(ctx, &obj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating %s in %s: %w", ri.Resource, ns, err)
	}

	c.cache.Delete(fmt.Sprintf("list:%s:%s", ns, ri.Resource))
	return nil
}

// ApplyObject updates an existing Kubernetes object from raw JSON or YAML data.
func (c *Client) ApplyObject(ctx context.Context, ns string, ri ResourceInfo, data []byte) error {
	jsonData := data
	if !json.Valid(data) {
		var err error
		jsonData, err = sigsyaml.YAMLToJSON(data)
		if err != nil {
			return fmt.Errorf("converting yaml to json: %w", err)
		}
	}

	var obj unstructured.Unstructured
	if err := json.Unmarshal(jsonData, &obj.Object); err != nil {
		return fmt.Errorf("unmarshalling object: %w", err)
	}

	name := obj.GetName()
	if name == "" {
		return fmt.Errorf("object must have a name")
	}

	_, err := c.dynamic.Resource(ri.GVR()).Namespace(ns).Update(ctx, &obj, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating %s/%s in %s: %w", ri.Resource, name, ns, err)
	}

	c.InvalidateObject(ns, ri.Resource, name)
	return nil
}

// GetObjectData returns the .data map from a ConfigMap or Secret.
// For secrets the base64-encoded values are decoded. For configmaps
// the string values are returned as raw bytes.
func (c *Client) GetObjectData(ctx context.Context, ns string, ri ResourceInfo, name string) (map[string][]byte, error) {
	key := fmt.Sprintf("data:%s:%s:%s", ns, ri.Resource, name)
	if v, ok := c.cache.Get(key); ok {
		return v.(map[string][]byte), nil
	}

	obj, err := c.dynamic.Resource(ri.GVR()).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting %s/%s in %s: %w", ri.Resource, name, ns, err)
	}

	result, err := extractData(obj, ri.Resource)
	if err != nil {
		return nil, err
	}

	c.cache.Set(key, result)
	return result, nil
}

// UpdateObjectDataKey performs a read-modify-write on a single key in
// the .data map of a ConfigMap or Secret.
func (c *Client) UpdateObjectDataKey(ctx context.Context, ns string, ri ResourceInfo, objName, dataKey string, value []byte) error {
	obj, err := c.dynamic.Resource(ri.GVR()).Namespace(ns).Get(ctx, objName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting %s/%s in %s: %w", ri.Resource, objName, ns, err)
	}

	dataField, _, _ := unstructured.NestedMap(obj.Object, "data")
	if dataField == nil {
		dataField = make(map[string]interface{})
	}

	if ri.Resource == "secrets" {
		dataField[dataKey] = base64.StdEncoding.EncodeToString(value)
	} else {
		dataField[dataKey] = string(value)
	}

	if err := unstructured.SetNestedMap(obj.Object, dataField, "data"); err != nil {
		return fmt.Errorf("setting data field: %w", err)
	}

	_, err = c.dynamic.Resource(ri.GVR()).Namespace(ns).Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating %s/%s in %s: %w", ri.Resource, objName, ns, err)
	}

	c.InvalidateObject(ns, ri.Resource, objName)
	return nil
}

// DeleteObject deletes a Kubernetes object by name.
func (c *Client) DeleteObject(ctx context.Context, ns string, ri ResourceInfo, name string) error {
	err := c.dynamic.Resource(ri.GVR()).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("deleting %s/%s in %s: %w", ri.Resource, name, ns, err)
	}

	c.InvalidateObject(ns, ri.Resource, name)
	return nil
}

// InvalidateObject removes cached data for a specific object.
func (c *Client) InvalidateObject(ns, resource, name string) {
	c.cache.Delete(fmt.Sprintf("yaml:%s:%s:%s", ns, resource, name))
	c.cache.Delete(fmt.Sprintf("json:%s:%s:%s", ns, resource, name))
	c.cache.Delete(fmt.Sprintf("describe:%s:%s:%s", ns, resource, name))
	c.cache.Delete(fmt.Sprintf("data:%s:%s:%s", ns, resource, name))
	c.cache.Delete(fmt.Sprintf("logs:%s:%s", ns, name))
	c.cache.Delete(fmt.Sprintf("list:%s:%s", ns, resource))
}

// extractData reads the .data map from an unstructured object and
// returns the values as raw bytes. Secret values are base64-decoded.
func extractData(obj *unstructured.Unstructured, resource string) (map[string][]byte, error) {
	dataField, _, _ := unstructured.NestedMap(obj.Object, "data")
	result := make(map[string][]byte, len(dataField))
	for k, v := range dataField {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if resource == "secrets" {
			decoded, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				return nil, fmt.Errorf("decoding secret key %q: %w", k, err)
			}
			result[k] = decoded
		} else {
			result[k] = []byte(s)
		}
	}
	return result, nil
}

func containsVerb(verbs metav1.Verbs, verb string) bool {
	for _, v := range verbs {
		if v == verb {
			return true
		}
	}
	return false
}

// formatDescription produces a kubectl-describe-style output from an
// unstructured object. This is a simplified version that covers the
// most useful fields.
func formatDescription(obj *unstructured.Unstructured, ri ResourceInfo) []byte {
	var buf bytes.Buffer

	fmt.Fprintf(&buf, "Name:         %s\n", obj.GetName())
	fmt.Fprintf(&buf, "Namespace:    %s\n", obj.GetNamespace())
	fmt.Fprintf(&buf, "Kind:         %s\n", ri.Kind)

	if apiVersion := obj.GetAPIVersion(); apiVersion != "" {
		fmt.Fprintf(&buf, "API Version:  %s\n", apiVersion)
	}

	if labels := obj.GetLabels(); len(labels) > 0 {
		fmt.Fprintf(&buf, "Labels:\n")
		keys := sortedKeys(labels)
		for _, k := range keys {
			fmt.Fprintf(&buf, "              %s=%s\n", k, labels[k])
		}
	} else {
		fmt.Fprintf(&buf, "Labels:       <none>\n")
	}

	if annotations := obj.GetAnnotations(); len(annotations) > 0 {
		fmt.Fprintf(&buf, "Annotations:\n")
		keys := sortedKeys(annotations)
		for _, k := range keys {
			fmt.Fprintf(&buf, "              %s=%s\n", k, annotations[k])
		}
	} else {
		fmt.Fprintf(&buf, "Annotations:  <none>\n")
	}

	fmt.Fprintf(&buf, "Created:      %s\n", obj.GetCreationTimestamp().Format(time.RFC3339))

	if spec, ok := obj.Object["spec"]; ok {
		specYAML, err := sigsyaml.Marshal(spec)
		if err == nil {
			fmt.Fprintf(&buf, "\nSpec:\n")
			for _, line := range strings.Split(string(specYAML), "\n") {
				if line != "" {
					fmt.Fprintf(&buf, "  %s\n", line)
				}
			}
		}
	}

	if status, ok := obj.Object["status"]; ok {
		statusYAML, err := sigsyaml.Marshal(status)
		if err == nil {
			fmt.Fprintf(&buf, "\nStatus:\n")
			for _, line := range strings.Split(string(statusYAML), "\n") {
				if line != "" {
					fmt.Fprintf(&buf, "  %s\n", line)
				}
			}
		}
	}

	return buf.Bytes()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
