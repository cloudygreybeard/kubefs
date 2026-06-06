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
	"testing"
)

func TestParseVerbsEmpty(t *testing.T) {
	v, err := ParseVerbs(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.ReadOnly() {
		t.Error("empty input should produce read-only verbs")
	}
}

func TestParseVerbsSingle(t *testing.T) {
	v, err := ParseVerbs([]string{"update"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Update {
		t.Error("Update should be true")
	}
	if v.Create || v.Delete {
		t.Error("Create and Delete should be false")
	}
}

func TestParseVerbsCSV(t *testing.T) {
	v, err := ParseVerbs([]string{"create,update,delete"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Create || !v.Update || !v.Delete {
		t.Errorf("all verbs should be true: create=%v update=%v delete=%v", v.Create, v.Update, v.Delete)
	}
}

func TestParseVerbsMultipleFlags(t *testing.T) {
	v, err := ParseVerbs([]string{"create", "delete"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Create || !v.Delete {
		t.Error("create and delete should be true")
	}
	if v.Update {
		t.Error("update should be false")
	}
}

func TestParseVerbsReadAccepted(t *testing.T) {
	v, err := ParseVerbs([]string{"read,update"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Update {
		t.Error("update should be true")
	}
	if v.ReadOnly() {
		t.Error("should not be read-only when update is enabled")
	}
}

func TestParseVerbsReadOnlyExplicit(t *testing.T) {
	v, err := ParseVerbs([]string{"read"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.ReadOnly() {
		t.Error("'read' alone should still be read-only (no mutating verbs)")
	}
}

func TestParseVerbsUnknown(t *testing.T) {
	_, err := ParseVerbs([]string{"patch"})
	if err == nil {
		t.Fatal("expected error for unknown verb")
	}
}

func TestParseVerbsCaseInsensitive(t *testing.T) {
	v, err := ParseVerbs([]string{"Update"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Update {
		t.Error("should handle uppercase input")
	}
}

func TestParseVerbsWhitespace(t *testing.T) {
	v, err := ParseVerbs([]string{" create , update "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Create || !v.Update {
		t.Error("should trim whitespace")
	}
}

func TestAllowedVerbsReadOnly(t *testing.T) {
	v := AllowedVerbs{}
	if !v.ReadOnly() {
		t.Error("zero-value should be read-only")
	}

	v.Create = true
	if v.ReadOnly() {
		t.Error("should not be read-only when create is enabled")
	}
}

func TestAllowedVerbsString(t *testing.T) {
	tests := []struct {
		verbs AllowedVerbs
		want  string
	}{
		{AllowedVerbs{}, "read-only"},
		{AllowedVerbs{Update: true}, "read,update"},
		{AllowedVerbs{Create: true, Update: true}, "read,create,update"},
		{AllowedVerbs{Create: true, Update: true, Delete: true}, "read,create,update,delete"},
		{AllowedVerbs{Delete: true}, "read,delete"},
	}

	for _, tc := range tests {
		if got := tc.verbs.String(); got != tc.want {
			t.Errorf("AllowedVerbs%+v.String() = %q, want %q", tc.verbs, got, tc.want)
		}
	}
}
