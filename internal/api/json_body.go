// SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// adminJSONBodyLimit bounds the JSON bodies of the administrative routes:
// every one is a handful of fields (58 §7 item 11: the role route and the
// collection routes read unbounded bodies). The group and account routes
// pass their own bounds.
const adminJSONBodyLimit int64 = 1 << 16

// decodeJSONBody is the one reader of a request body in this package
// (pinned: no other function touches r.Body at all — not a decoder, a
// ReadAll, a LimitReader or a helper handed the body): at
// most limit bytes (http.MaxBytesReader), one value, then nothing but
// whitespace. The caller answers the error with its own body-shape
// message.
//
// The trailing-data rule is "a second Decode answers io.EOF" (0.29.104,
// Copilot on PR #238). Decoder.More was the check before; it peeks one
// byte and answers false for a stray `}` or `]` AND on any reader error,
// so `{"admin":true}}` and a value followed by whitespace past the limit
// both reached the writers. A second value is errTrailingJSON; a stray
// delimiter is the decoder's syntax error; the bounded reader's own
// error (the limit) is returned as it is — every one is a refusal.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(v); err != nil {
		return err
	}
	switch err := dec.Decode(new(json.RawMessage)); {
	case errors.Is(err, io.EOF):
		return nil
	case err == nil:
		return errTrailingJSON
	default:
		return err
	}
}

type trailingJSONError struct{}

func (trailingJSONError) Error() string { return "trailing data after the JSON body" }

var errTrailingJSON error = trailingJSONError{}
