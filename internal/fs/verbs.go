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
	"fmt"
	"strings"
)

// AllowedVerbs controls which mutating operations the filesystem permits.
// Read access is always enabled and cannot be disabled.
type AllowedVerbs struct {
	Create bool
	Update bool
	Delete bool
}

// ReadOnly returns true if no mutating verbs are enabled.
func (v AllowedVerbs) ReadOnly() bool {
	return !v.Create && !v.Update && !v.Delete
}

// String returns a human-readable summary of the enabled verbs.
func (v AllowedVerbs) String() string {
	if v.ReadOnly() {
		return "read-only"
	}
	var verbs []string
	if v.Create {
		verbs = append(verbs, "create")
	}
	if v.Update {
		verbs = append(verbs, "update")
	}
	if v.Delete {
		verbs = append(verbs, "delete")
	}
	return "read," + strings.Join(verbs, ",")
}

// ParseVerbs parses a slice of verb strings into AllowedVerbs.
// Valid verbs are "create", "update", and "delete". The verb "read" is
// accepted but has no effect (read is always enabled). Unknown verbs
// produce an error.
func ParseVerbs(input []string) (AllowedVerbs, error) {
	var v AllowedVerbs
	for _, raw := range input {
		for _, verb := range strings.Split(raw, ",") {
			verb = strings.TrimSpace(strings.ToLower(verb))
			if verb == "" {
				continue
			}
			switch verb {
			case "create":
				v.Create = true
			case "update":
				v.Update = true
			case "delete":
				v.Delete = true
			case "read":
				// always on
			default:
				return AllowedVerbs{}, fmt.Errorf("unknown verb %q (valid: create, update, delete)", verb)
			}
		}
	}
	return v, nil
}
