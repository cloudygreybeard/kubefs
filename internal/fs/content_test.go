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
	"reflect"
	"testing"
)

func TestActionFiles(t *testing.T) {
	tests := []struct {
		resource string
		want     []string
	}{
		{"pods", []string{"yaml", "json", "describe", "logs"}},
		{"deployments", []string{"yaml", "json", "describe"}},
		{"services", []string{"yaml", "json", "describe"}},
		{"configmaps", []string{"yaml", "json", "describe"}},
	}

	for _, tc := range tests {
		t.Run(tc.resource, func(t *testing.T) {
			got := ActionFiles(tc.resource)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ActionFiles(%q) = %v, want %v", tc.resource, got, tc.want)
			}
		})
	}
}

func TestHasDataDir(t *testing.T) {
	tests := []struct {
		resource string
		want     bool
	}{
		{"configmaps", true},
		{"secrets", true},
		{"pods", false},
		{"deployments", false},
		{"services", false},
	}

	for _, tc := range tests {
		t.Run(tc.resource, func(t *testing.T) {
			if got := HasDataDir(tc.resource); got != tc.want {
				t.Errorf("HasDataDir(%q) = %v, want %v", tc.resource, got, tc.want)
			}
		})
	}
}
