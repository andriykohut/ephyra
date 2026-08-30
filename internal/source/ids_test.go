package source

import "testing"

func TestCanonID(t *testing.T) {
	cases := map[string]string{
		"11111111-2222-3333-4444-555555555555":    "11111111222233334444555555555555",
		"  00000000-0000-0000-0000-00000000000A ": "0000000000000000000000000000000a",
		"already1111111122223333444455555555555":  "already1111111122223333444455555555555",
		"":                                        "",
	}
	for in, want := range cases {
		if got := CanonID(in); got != want {
			t.Errorf("CanonID(%q) = %q, want %q", in, got, want)
		}
	}
}
