// SPDX-License-Identifier: GPL-3.0-or-later
package observability

import (
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"io"
)

func Redact(text string, secrets []string) string { return redaction.Redact(text, secrets) }
func NewStreamRedactor(out io.Writer, secrets []string) (*redaction.Stream, error) {
	return redaction.NewStream(out, secrets)
}
