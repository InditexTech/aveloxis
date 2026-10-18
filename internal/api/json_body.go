// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"net/http"
)

// adminJSONBodyLimit bounds the JSON bodies of the administrative and
// group routes: every one is a handful of fields (58 §7 item 11: the role
// route and the collection routes read unbounded bodies).
const adminJSONBodyLimit int64 = 1 << 16

// decodeJSONBody is the one JSON body reader for the routes that take a
// small object: bounded by limit (http.MaxBytesReader), one value, no
// trailing data. The caller answers the error with its own body-shape
// message.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(v); err != nil {
		return err
	}
	// A second value after the object is not a body of this shape.
	if dec.More() {
		return errTrailingJSON
	}
	return nil
}

type trailingJSONError struct{}

func (trailingJSONError) Error() string { return "trailing data after the JSON body" }

var errTrailingJSON error = trailingJSONError{}
