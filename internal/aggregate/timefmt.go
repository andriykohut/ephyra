package aggregate

import "time"

// rfc3339 renders a timestamp for the store; the zero time becomes "" (which the
// store writes as SQL NULL).
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
