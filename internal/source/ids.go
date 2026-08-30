package source

import "strings"

// CanonID normalizes a Jellyfin GUID to Ephyra's canonical form: lowercase hex,
// no dashes. jellyfin.db uses dashed-uppercase; the Playback Reporting plugin
// uses dashless-lowercase. Everything Ephyra stores is canonical.
func CanonID(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}
