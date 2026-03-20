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
	"encoding/base64"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestResourceInfoGVR(t *testing.T) {
	ri := ResourceInfo{
		Group:    "apps",
		Version:  "v1",
		Resource: "deployments",
		Kind:     "Deployment",
	}
	gvr := ri.GVR()

	if gvr.Group != "apps" {
		t.Errorf("Group = %q, want %q", gvr.Group, "apps")
	}
	if gvr.Version != "v1" {
		t.Errorf("Version = %q, want %q", gvr.Version, "v1")
	}
	if gvr.Resource != "deployments" {
		t.Errorf("Resource = %q, want %q", gvr.Resource, "deployments")
	}
}

func TestResourceInfoGVRCoreGroup(t *testing.T) {
	ri := ResourceInfo{Group: "", Version: "v1", Resource: "pods"}
	gvr := ri.GVR()

	if gvr.Group != "" {
		t.Errorf("Group = %q, want empty string for core API", gvr.Group)
	}
}

func TestExtractDataConfigMap(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"data": map[string]interface{}{
				"app.conf": "key=value\n",
				"db.url":   "postgres://localhost/db",
			},
		},
	}

	result, err := extractData(obj, "configmaps")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("got %d keys, want 2", len(result))
	}
	if string(result["app.conf"]) != "key=value\n" {
		t.Errorf("app.conf = %q, want %q", result["app.conf"], "key=value\n")
	}
	if string(result["db.url"]) != "postgres://localhost/db" {
		t.Errorf("db.url = %q, want %q", result["db.url"], "postgres://localhost/db")
	}
}

func TestExtractDataSecret(t *testing.T) {
	password := "s3cret!"
	encoded := base64.StdEncoding.EncodeToString([]byte(password))

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"data": map[string]interface{}{
				"password": encoded,
			},
		},
	}

	result, err := extractData(obj, "secrets")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result["password"]) != password {
		t.Errorf("password = %q, want %q", result["password"], password)
	}
}

func TestExtractDataSecretBinary(t *testing.T) {
	binary := []byte{0x00, 0x01, 0x02, 0xFF}
	encoded := base64.StdEncoding.EncodeToString(binary)

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"data": map[string]interface{}{
				"cert": encoded,
			},
		},
	}

	result, err := extractData(obj, "secrets")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result["cert"]) != len(binary) {
		t.Fatalf("cert length = %d, want %d", len(result["cert"]), len(binary))
	}
	for i, b := range result["cert"] {
		if b != binary[i] {
			t.Errorf("cert[%d] = %x, want %x", i, b, binary[i])
		}
	}
}

func TestExtractDataEmpty(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{},
	}

	result, err := extractData(obj, "configmaps")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("got %d keys, want 0", len(result))
	}
}

func TestExtractDataSkipsNonString(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"data": map[string]interface{}{
				"valid":   "hello",
				"invalid": true,
			},
		},
	}

	result, err := extractData(obj, "configmaps")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d keys, want 1", len(result))
	}
	if string(result["valid"]) != "hello" {
		t.Errorf("valid = %q, want %q", result["valid"], "hello")
	}
}

func TestExtractDataSecretInvalidBase64(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"data": map[string]interface{}{
				"bad": "not-valid-base64!!!",
			},
		},
	}

	_, err := extractData(obj, "secrets")
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
	if !strings.Contains(err.Error(), "decoding secret key") {
		t.Errorf("error = %q, want it to mention decoding", err.Error())
	}
}

func TestFormatDescriptionBasic(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name":              "my-config",
				"namespace":         "default",
				"creationTimestamp": "2026-01-15T10:00:00Z",
			},
		},
	}
	ri := ResourceInfo{Kind: "ConfigMap"}

	out := string(formatDescription(obj, ri))

	for _, want := range []string{
		"Name:         my-config",
		"Namespace:    default",
		"Kind:         ConfigMap",
		"API Version:  v1",
		"Labels:       <none>",
		"Annotations:  <none>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestFormatDescriptionWithLabels(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":              "test",
				"namespace":         "ns",
				"creationTimestamp": "2026-01-15T10:00:00Z",
				"labels": map[string]interface{}{
					"app": "web",
					"env": "prod",
				},
			},
		},
	}
	ri := ResourceInfo{Kind: "Pod"}

	out := string(formatDescription(obj, ri))

	if strings.Contains(out, "Labels:       <none>") {
		t.Error("should not show <none> when labels exist")
	}
	if !strings.Contains(out, "app=web") {
		t.Error("missing label app=web")
	}
	if !strings.Contains(out, "env=prod") {
		t.Error("missing label env=prod")
	}
}

func TestFormatDescriptionWithAnnotations(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":              "test",
				"namespace":         "ns",
				"creationTimestamp": "2026-01-15T10:00:00Z",
				"annotations": map[string]interface{}{
					"note": "important",
				},
			},
		},
	}
	ri := ResourceInfo{Kind: "Service"}

	out := string(formatDescription(obj, ri))

	if strings.Contains(out, "Annotations:  <none>") {
		t.Error("should not show <none> when annotations exist")
	}
	if !strings.Contains(out, "note=important") {
		t.Error("missing annotation note=important")
	}
}

func TestFormatDescriptionWithSpec(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":              "test",
				"namespace":         "ns",
				"creationTimestamp": "2026-01-15T10:00:00Z",
			},
			"spec": map[string]interface{}{
				"replicas": int64(3),
			},
		},
	}
	ri := ResourceInfo{Kind: "Deployment"}

	out := string(formatDescription(obj, ri))

	if !strings.Contains(out, "Spec:") {
		t.Error("missing Spec section")
	}
	if !strings.Contains(out, "replicas: 3") {
		t.Error("missing replicas in spec")
	}
}

func TestFormatDescriptionWithStatus(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":              "test",
				"namespace":         "ns",
				"creationTimestamp": "2026-01-15T10:00:00Z",
			},
			"status": map[string]interface{}{
				"phase": "Running",
			},
		},
	}
	ri := ResourceInfo{Kind: "Pod"}

	out := string(formatDescription(obj, ri))

	if !strings.Contains(out, "Status:") {
		t.Error("missing Status section")
	}
	if !strings.Contains(out, "phase: Running") {
		t.Error("missing phase in status")
	}
}

func TestInvalidateObject(t *testing.T) {
	c := &Client{cache: NewCache(time.Minute)}

	keys := []string{
		"yaml:ns:pods:mypod",
		"json:ns:pods:mypod",
		"describe:ns:pods:mypod",
		"data:ns:pods:mypod",
		"logs:ns:mypod",
		"list:ns:pods",
	}
	for _, k := range keys {
		c.cache.Set(k, "cached")
	}
	c.cache.Set("yaml:ns:pods:otherpod", "should-survive")

	c.InvalidateObject("ns", "pods", "mypod")

	for _, k := range keys {
		if _, ok := c.cache.Get(k); ok {
			t.Errorf("key %q should have been invalidated", k)
		}
	}
	if _, ok := c.cache.Get("yaml:ns:pods:otherpod"); !ok {
		t.Error("otherpod cache should not have been invalidated")
	}
}

func TestContainsVerb(t *testing.T) {
	verbs := metav1.Verbs{"get", "list", "watch"}

	if !containsVerb(verbs, "get") {
		t.Error("should contain 'get'")
	}
	if !containsVerb(verbs, "list") {
		t.Error("should contain 'list'")
	}
	if containsVerb(verbs, "delete") {
		t.Error("should not contain 'delete'")
	}
	if containsVerb(nil, "get") {
		t.Error("nil verbs should not contain anything")
	}
}
