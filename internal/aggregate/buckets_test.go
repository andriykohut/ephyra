package aggregate

import "testing"

func TestResolutionBucket(t *testing.T) {
	cases := map[int]string{4096: "4K", 3840: "4K", 3200: "4K", 1920: "1080p", 1400: "1080p", 1280: "720p", 1000: "720p", 720: "SD", 1: "SD", 0: "Unknown"}
	for w, want := range cases {
		if got := ResolutionBucket(w); got != want {
			t.Errorf("ResolutionBucket(%d) = %q, want %q", w, got, want)
		}
	}
}

func TestCodecBucket(t *testing.T) {
	cases := map[string]string{"h264": "H.264", "AVC": "H.264", "hevc": "HEVC", "h265": "HEVC", "av1": "AV1", "vp9": "VP9", "": "Unknown", "weirdcodec": "WEIRDCODEC"}
	for in, want := range cases {
		if got := CodecBucket(in); got != want {
			t.Errorf("CodecBucket(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHDRBucket(t *testing.T) {
	p := 7
	zero := 0
	if got := HDRBucket("smpte2084", &p, true); got != "Dolby Vision" {
		t.Errorf("DV precedence: %q", got)
	}
	if got := HDRBucket("smpte2084", nil, true); got != "HDR10" {
		t.Errorf("HDR10: %q", got)
	}
	if got := HDRBucket("arib-std-b67", nil, true); got != "HLG" {
		t.Errorf("HLG: %q", got)
	}
	if got := HDRBucket("bt709", nil, true); got != "SDR" {
		t.Errorf("SDR: %q", got)
	}
	if got := HDRBucket("", nil, true); got != "SDR" {
		t.Errorf("blank transfer w/ video = SDR: %q", got)
	}
	if got := HDRBucket("", nil, false); got != "Unknown" {
		t.Errorf("no video = Unknown: %q", got)
	}
	if got := HDRBucket("smpte2084", &zero, true); got != "HDR10" {
		t.Errorf("DvProfile 0 is not DV: %q", got)
	}
}

func TestDecade(t *testing.T) {
	for in, want := range map[int]string{1994: "1990s", 2000: "2000s", 2019: "2010s", 2022: "2020s", 0: "Unknown", -5: "Unknown"} {
		if got := Decade(in); got != want {
			t.Errorf("Decade(%d) = %q, want %q", in, got, want)
		}
	}
}
