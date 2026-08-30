package aggregate

import (
	"fmt"
	"strings"
)

// ResolutionBucket maps a primary-video width to a label. Thresholds are
// deliberately loose so odd crops still land somewhere sensible.
func ResolutionBucket(width int) string {
	switch {
	case width >= 3200:
		return "4K"
	case width >= 1400:
		return "1080p"
	case width >= 1000:
		return "720p"
	case width > 0:
		return "SD"
	default:
		return "Unknown"
	}
}

func CodecBucket(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "h264", "avc":
		return "H.264"
	case "hevc", "h265":
		return "HEVC"
	case "av1":
		return "AV1"
	case "vp9":
		return "VP9"
	case "mpeg2video":
		return "MPEG-2"
	case "vc1":
		return "VC-1"
	case "":
		return "Unknown"
	default:
		return strings.ToUpper(strings.TrimSpace(raw))
	}
}

// HDRBucket. Dolby Vision wins if a profile is set; otherwise the colour
// transfer decides. A blank transfer on an item that does have video is
// treated as SDR; no video at all is Unknown.
func HDRBucket(colorTransfer string, dvProfile *int, hasVideo bool) string {
	if dvProfile != nil && *dvProfile > 0 {
		return "Dolby Vision"
	}
	switch strings.ToLower(strings.TrimSpace(colorTransfer)) {
	case "smpte2084":
		return "HDR10"
	case "arib-std-b67":
		return "HLG"
	}
	if !hasVideo {
		return "Unknown"
	}
	return "SDR"
}

func Decade(year int) string {
	if year <= 0 {
		return "Unknown"
	}
	return fmt.Sprintf("%ds", (year/10)*10)
}
