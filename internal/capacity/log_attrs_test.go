// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package capacity

import (
	"reflect"
	"testing"
)

// LogAttrs is the one vocabulary of a logged refusal: these keys, in this
// order, from these fields (v0.29.96).
func TestLogAttrsIsTheOneVocabulary(t *testing.T) {
	ex := &Exceeded{Quota: QuotaRepoLinksPerDay, Used: 7, Allowed: 5, Wanted: 3}
	want := []any{"quota", QuotaRepoLinksPerDay, "used", 7, "allowed", 5, "wanted", 3}
	if got := ex.LogAttrs(); !reflect.DeepEqual(got, want) {
		t.Errorf("LogAttrs() = %v, want %v", got, want)
	}
}
