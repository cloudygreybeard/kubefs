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

package main

// coreResponses maps "namespace/resource[/name]" to JSON responses.
var coreResponses = map[string]string{
	// --- pods ---
	"default/pods": podListResponse,
	"default/pods/nginx-7f8b6c5d9-xk2p4": `{
  "apiVersion": "v1",
  "kind": "Pod",
  "metadata": {
    "name": "nginx-7f8b6c5d9-xk2p4",
    "namespace": "default",
    "creationTimestamp": "2026-03-17T10:00:00Z",
    "labels": {"app": "nginx", "pod-template-hash": "7f8b6c5d9"}
  },
  "spec": {
    "containers": [{"name": "nginx", "image": "nginx:1.27", "ports": [{"containerPort": 80}]}]
  },
  "status": {"phase": "Running"}
}`,
	"default/pods/redis-master-0": `{
  "apiVersion": "v1",
  "kind": "Pod",
  "metadata": {
    "name": "redis-master-0",
    "namespace": "default",
    "creationTimestamp": "2026-03-16T08:30:00Z",
    "labels": {"app": "redis", "role": "master"}
  },
  "spec": {
    "containers": [{"name": "redis", "image": "redis:7-alpine", "ports": [{"containerPort": 6379}]}]
  },
  "status": {"phase": "Running"}
}`,
	"default/pods/nginx-7f8b6c5d9-xk2p4/log": `2026/03/17 10:00:01 [notice] 1#1: nginx/1.27.0
2026/03/17 10:00:01 [notice] 1#1: built by gcc 12.2.0
2026/03/17 10:00:01 [notice] 1#1: OS: Linux 6.1.0
2026/03/17 10:00:01 [notice] 1#1: start worker processes
10.0.0.1 - - [17/Mar/2026:10:05:23 +0000] "GET / HTTP/1.1" 200 615
10.0.0.1 - - [17/Mar/2026:10:10:45 +0000] "GET /healthz HTTP/1.1" 200 2
`,

	// --- configmaps ---
	"default/configmaps": configMapListResponse,
	"default/configmaps/app-config": `{
  "apiVersion": "v1",
  "kind": "ConfigMap",
  "metadata": {
    "name": "app-config",
    "namespace": "default",
    "creationTimestamp": "2026-03-15T12:00:00Z",
    "labels": {"app": "myapp"}
  },
  "data": {
    "database.url": "postgres://db.internal:5432/myapp",
    "log.level": "info",
    "max.retries": "3"
  }
}`,

	// --- secrets ---
	"default/secrets": "", // populated at startup by initTLSResponses
	"default/secrets/nginx-tls": "", // populated at startup by initTLSResponses

	// --- services ---
	"default/services": serviceListResponse,
	"default/services/nginx-svc": `{
  "apiVersion": "v1",
  "kind": "Service",
  "metadata": {
    "name": "nginx-svc",
    "namespace": "default",
    "creationTimestamp": "2026-03-17T10:00:00Z"
  },
  "spec": {
    "selector": {"app": "nginx"},
    "ports": [{"port": 80, "targetPort": 80, "protocol": "TCP"}],
    "type": "ClusterIP"
  }
}`,
}

const namespacesResponse = `{
  "apiVersion": "v1",
  "kind": "NamespaceList",
  "items": [
    {"metadata": {"name": "default", "creationTimestamp": "2026-01-01T00:00:00Z"}},
    {"metadata": {"name": "kube-system", "creationTimestamp": "2026-01-01T00:00:00Z"}},
    {"metadata": {"name": "monitoring", "creationTimestamp": "2026-02-01T00:00:00Z"}}
  ]
}`

const apiVersionsResponse = `{
  "kind": "APIVersions",
  "versions": ["v1"],
  "serverAddressByClientCIDRs": [{"clientCIDR": "0.0.0.0/0", "serverAddress": "localhost:6443"}]
}`

const apiGroupsResponse = `{
  "kind": "APIGroupList",
  "apiVersion": "v1",
  "groups": [
    {
      "name": "apps",
      "versions": [{"groupVersion": "apps/v1", "version": "v1"}],
      "preferredVersion": {"groupVersion": "apps/v1", "version": "v1"}
    }
  ]
}`

const coreV1ResourcesResponse = `{
  "kind": "APIResourceList",
  "groupVersion": "v1",
  "resources": [
    {"name": "namespaces", "singularName": "", "namespaced": false, "kind": "Namespace", "verbs": ["get","list"]},
    {"name": "pods", "singularName": "", "namespaced": true, "kind": "Pod", "verbs": ["get","list","create","update","delete"]},
    {"name": "pods/log", "singularName": "", "namespaced": true, "kind": "Pod", "verbs": ["get"]},
    {"name": "configmaps", "singularName": "", "namespaced": true, "kind": "ConfigMap", "verbs": ["get","list","create","update","delete"]},
    {"name": "secrets", "singularName": "", "namespaced": true, "kind": "Secret", "verbs": ["get","list","create","update","delete"]},
    {"name": "services", "singularName": "", "namespaced": true, "kind": "Service", "verbs": ["get","list","create","update","delete"]}
  ]
}`

const appsV1ResourcesResponse = `{
  "kind": "APIResourceList",
  "groupVersion": "apps/v1",
  "resources": [
    {"name": "deployments", "singularName": "", "namespaced": true, "kind": "Deployment", "verbs": ["get","list","create","update","delete"]},
    {"name": "statefulsets", "singularName": "", "namespaced": true, "kind": "StatefulSet", "verbs": ["get","list","create","update","delete"]}
  ]
}`

var appsResponses = map[string]string{
	"default/deployments": deploymentListResponse,
	"default/deployments/nginx": `{
  "apiVersion": "apps/v1",
  "kind": "Deployment",
  "metadata": {
    "name": "nginx",
    "namespace": "default",
    "creationTimestamp": "2026-03-17T10:00:00Z",
    "labels": {"app": "nginx"},
    "generation": 3
  },
  "spec": {
    "replicas": 2,
    "selector": {"matchLabels": {"app": "nginx"}},
    "template": {
      "metadata": {"labels": {"app": "nginx"}},
      "spec": {
        "containers": [
          {
            "name": "nginx",
            "image": "nginx:1.27",
            "ports": [{"containerPort": 80}],
            "resources": {
              "requests": {"cpu": "100m", "memory": "128Mi"},
              "limits": {"cpu": "250m", "memory": "256Mi"}
            }
          }
        ]
      }
    }
  },
  "status": {
    "replicas": 2,
    "readyReplicas": 2,
    "availableReplicas": 2,
    "updatedReplicas": 2
  }
}`,
}

const deploymentListResponse = `{
  "apiVersion": "apps/v1",
  "kind": "DeploymentList",
  "items": [
    {
      "metadata": {"name": "nginx", "namespace": "default", "creationTimestamp": "2026-03-17T10:00:00Z", "labels": {"app": "nginx"}},
      "spec": {"replicas": 2, "selector": {"matchLabels": {"app": "nginx"}}, "template": {"spec": {"containers": [{"name": "nginx", "image": "nginx:1.27"}]}}},
      "status": {"replicas": 2, "readyReplicas": 2, "availableReplicas": 2}
    }
  ]
}`

const podListResponse = `{
  "apiVersion": "v1",
  "kind": "PodList",
  "items": [
    {
      "metadata": {"name": "nginx-7f8b6c5d9-xk2p4", "namespace": "default", "creationTimestamp": "2026-03-17T10:00:00Z", "labels": {"app": "nginx"}},
      "spec": {"containers": [{"name": "nginx", "image": "nginx:1.27"}]},
      "status": {"phase": "Running"}
    },
    {
      "metadata": {"name": "redis-master-0", "namespace": "default", "creationTimestamp": "2026-03-16T08:30:00Z", "labels": {"app": "redis"}},
      "spec": {"containers": [{"name": "redis", "image": "redis:7-alpine"}]},
      "status": {"phase": "Running"}
    }
  ]
}`

const configMapListResponse = `{
  "apiVersion": "v1",
  "kind": "ConfigMapList",
  "items": [
    {
      "metadata": {"name": "app-config", "namespace": "default", "creationTimestamp": "2026-03-15T12:00:00Z", "labels": {"app": "myapp"}},
      "data": {"database.url": "postgres://db.internal:5432/myapp", "log.level": "info", "max.retries": "3"}
    }
  ]
}`

// initTLSResponses generates a self-signed TLS certificate at runtime
// and populates the nginx-tls secret responses. This avoids storing
// any key material in source code.
func initTLSResponses(tls tlsCredential) {
	coreResponses["default/secrets/nginx-tls"] = `{
  "apiVersion": "v1",
  "kind": "Secret",
  "type": "kubernetes.io/tls",
  "metadata": {
    "name": "nginx-tls",
    "namespace": "default",
    "creationTimestamp": "2026-03-15T12:00:00Z",
    "labels": {"app": "nginx"},
    "annotations": {"cert-manager.io/issuer-name": "letsencrypt-prod"}
  },
  "data": {
    "tls.crt": "` + tls.CertBase64 + `",
    "tls.key": "` + tls.KeyBase64 + `"
  }
}`
	coreResponses["default/secrets"] = `{
  "apiVersion": "v1",
  "kind": "SecretList",
  "items": [
    {
      "metadata": {"name": "nginx-tls", "namespace": "default", "creationTimestamp": "2026-03-15T12:00:00Z", "labels": {"app": "nginx"}, "annotations": {"cert-manager.io/issuer-name": "letsencrypt-prod"}},
      "type": "kubernetes.io/tls",
      "data": {"tls.crt": "` + tls.CertBase64 + `", "tls.key": "` + tls.KeyBase64 + `"}
    }
  ]
}`
}

const serviceListResponse = `{
  "apiVersion": "v1",
  "kind": "ServiceList",
  "items": [
    {
      "metadata": {"name": "nginx-svc", "namespace": "default", "creationTimestamp": "2026-03-17T10:00:00Z"},
      "spec": {"selector": {"app": "nginx"}, "ports": [{"port": 80, "targetPort": 80}], "type": "ClusterIP"}
    }
  ]
}`
