package live

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Summary struct {
	Streams         int   `json:"streams"`
	Transcodes      int   `json:"transcodes"`
	OutboundBitrate int64 `json:"outbound_bitrate"`
	Capacity        *int  `json:"capacity"`
}

type VideoSource struct {
	Codec   string `json:"codec"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Range   string `json:"range"`
	Bitrate int64  `json:"bitrate"`
}

type AudioSource struct {
	Codec    string `json:"codec"`
	Channels int    `json:"channels"`
	Layout   string `json:"layout"`
	Bitrate  int64  `json:"bitrate"`
}

type SourceStreams struct {
	Video VideoSource `json:"video"`
	Audio AudioSource `json:"audio"`
}

type Backdrop struct {
	ItemID string `json:"item_id"`
	Tag    string `json:"tag"`
}

type Art struct {
	PrimaryTag string    `json:"primary_tag"`
	Backdrop   *Backdrop `json:"backdrop"`
}

type Transcode struct {
	Bitrate       int64    `json:"bitrate"`
	Container     string   `json:"container"`
	Video         string   `json:"video"`
	Audio         string   `json:"audio"`
	HW            string   `json:"hw"`
	CompletionPct float64  `json:"completion_pct"`
	Reasons       []string `json:"reasons"`
}

type Session struct {
	SessionID     string        `json:"session_id"`
	User          string        `json:"user"`
	Type          string        `json:"type"`
	Title         string        `json:"title"`
	Series        string        `json:"series"`
	SeasonEpisode string        `json:"season_episode"`
	ItemID        string        `json:"item_id"`
	Art           Art           `json:"art"`
	PlayMethod    string        `json:"play_method"`
	Paused        bool          `json:"paused"`
	PositionSec   int64         `json:"position_sec"`
	RuntimeSec    int64         `json:"runtime_sec"`
	ProgressPct   float64       `json:"progress_pct"`
	IsRemote      bool          `json:"is_remote"`
	Client        string        `json:"client"`
	Device        string        `json:"device"`
	Source        SourceStreams `json:"source"`
	Transcode     *Transcode    `json:"transcode"`
}

type Snapshot struct {
	Server   ServerInfo `json:"server"`
	Degraded bool       `json:"degraded"`
	Summary  Summary    `json:"summary"`
	Sessions []Session  `json:"sessions"`
}

type Event struct {
	Kind string
	Data any
}

// degradedPayload is used in Plan 3 (Now Playing) for SSE events.
// nolint:unused
type degradedPayload struct {
	Degraded bool `json:"degraded"`
}
