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

// mock-kubeapi serves canned Kubernetes API responses. It provides
// enough of the API surface for kubefs to mount and display namespaces,
// resource types, objects, and configmap/secret data. It can also be
// used standalone for developing or testing any tool that talks to the
// Kubernetes API.
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

func main() {
	addr := ":6443"
	if v := os.Getenv("MOCK_API_ADDR"); v != "" {
		addr = v
	}

	tls, err := generateTLS("nginx.default.svc.cluster.local")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock-kubeapi: generating TLS credential: %v\n", err)
		os.Exit(1)
	}
	initTLSResponses(tls)

	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/namespaces", jsonHandler(namespacesResponse))
	mux.HandleFunc("/api/v1/", coreV1Handler)
	mux.HandleFunc("/api/", apiHandler)
	mux.HandleFunc("/apis/", apisHandler)
	mux.HandleFunc("/apis", jsonHandler(apiGroupsResponse))

	log.Printf("mock-kubeapi listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "mock-kubeapi: %v\n", err)
		os.Exit(1)
	}
}

func jsonHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}
}

func apiHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api" || r.URL.Path == "/api/" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, apiVersionsResponse)
		return
	}
	http.NotFound(w, r)
}

func apisHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if path == "/apis/apps/v1" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, appsV1ResourcesResponse)
		return
	}

	// /apis/apps/v1/namespaces/NS/RESOURCE[/NAME]
	trimmed := strings.TrimPrefix(path, "/apis/apps/v1/")
	if trimmed != path && trimmed != "" {
		parts := strings.Split(trimmed, "/")
		if len(parts) >= 3 && parts[0] == "namespaces" {
			ns := parts[1]
			resource := parts[2]
			name := ""
			if len(parts) >= 4 {
				name = parts[3]
			}
			serveAppsResource(w, r, ns, resource, name)
			return
		}
	}

	http.NotFound(w, r)
}

// coreV1Handler routes /api/v1/namespaces/NS/RESOURCE[/NAME] requests.
func coreV1Handler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")

	if path == "" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, coreV1ResourcesResponse)
		return
	}

	parts := strings.Split(path, "/")
	// namespaces/NS/resource[/name]
	if len(parts) >= 3 && parts[0] == "namespaces" {
		ns := parts[1]
		resource := parts[2]
		name := ""
		if len(parts) >= 4 {
			name = parts[3]
		}
		serveCoreResource(w, r, ns, resource, name)
		return
	}

	http.NotFound(w, r)
}

var responseMu sync.RWMutex

func serveCoreResource(w http.ResponseWriter, r *http.Request, ns, resource, name string) {
	serveResource(w, r, coreResponses, ns, resource, name)
}

func serveAppsResource(w http.ResponseWriter, r *http.Request, ns, resource, name string) {
	serveResource(w, r, appsResponses, ns, resource, name)
}

func serveResource(w http.ResponseWriter, r *http.Request, store map[string]string, ns, resource, name string) {
	w.Header().Set("Content-Type", "application/json")

	key := ns + "/" + resource
	if name != "" {
		key += "/" + name
	}

	if r.Method == http.MethodPut && name != "" {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"kind":"Status","status":"Failure","message":"read body: %v","code":500}`, err)
			return
		}
		responseMu.Lock()
		store[key] = string(body)
		responseMu.Unlock()
		log.Printf("PUT %s (%d bytes)", key, len(body))
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return
	}

	responseMu.RLock()
	resp, ok := store[key]
	responseMu.RUnlock()

	if ok {
		fmt.Fprint(w, resp)
		return
	}

	if name == "" {
		fmt.Fprintf(w, `{"apiVersion":"v1","kind":"List","items":[]}`)
		return
	}

	w.WriteHeader(http.StatusNotFound)
	fmt.Fprintf(w, `{"kind":"Status","status":"Failure","message":"%s %q not found","reason":"NotFound","code":404}`, resource, name)
}
