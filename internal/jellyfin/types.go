package jellyfin

type ServerInfo struct {
	Name    string `json:"ServerName"`
	Version string `json:"Version"`
}

type ImageKind string

const (
	ImagePrimary  ImageKind = "Primary"
	ImageBackdrop ImageKind = "Backdrop"
)

type RawSession struct {
	ID              string              `json:"Id"`
	UserID          string              `json:"UserId"`
	UserName        string              `json:"UserName"`
	Client          string              `json:"Client"`
	DeviceName      string              `json:"DeviceName"`
	RemoteEndPoint  string              `json:"RemoteEndPoint"`
	PlayState       *RawPlayState       `json:"PlayState"`
	TranscodingInfo *RawTranscodingInfo `json:"TranscodingInfo"`
	NowPlayingItem  *RawItem            `json:"NowPlayingItem"`
}

type RawPlayState struct {
	PositionTicks    int64  `json:"PositionTicks"`
	IsPaused         bool   `json:"IsPaused"`
	PlayMethod       string `json:"PlayMethod"`
	AudioStreamIndex *int   `json:"AudioStreamIndex"`
}

type RawTranscodingInfo struct {
	Bitrate                  int64    `json:"Bitrate"`
	Container                string   `json:"Container"`
	VideoCodec               string   `json:"VideoCodec"`
	AudioCodec               string   `json:"AudioCodec"`
	IsVideoDirect            bool     `json:"IsVideoDirect"`
	IsAudioDirect            bool     `json:"IsAudioDirect"`
	TranscodeReasons         []string `json:"TranscodeReasons"`
	CompletionPercentage     float64  `json:"CompletionPercentage"`
	HardwareAccelerationType string   `json:"HardwareAccelerationType"`
}

type RawItem struct {
	ID                      string            `json:"Id"`
	Name                    string            `json:"Name"`
	Type                    string            `json:"Type"`
	MediaType               string            `json:"MediaType"`
	Container               string            `json:"Container"`
	SeriesName              string            `json:"SeriesName"`
	RunTimeTicks            int64             `json:"RunTimeTicks"`
	IndexNumber             *int              `json:"IndexNumber"`
	ParentIndexNumber       *int              `json:"ParentIndexNumber"`
	ProductionYear          *int              `json:"ProductionYear"`
	ImageTags               map[string]string `json:"ImageTags"`
	ParentBackdropItemId    string            `json:"ParentBackdropItemId"`
	ParentBackdropImageTags []string          `json:"ParentBackdropImageTags"`
	MediaStreams            []RawStream       `json:"MediaStreams"`
}

type RawStream struct {
	Type           string `json:"Type"`
	Index          int    `json:"Index"`
	Codec          string `json:"Codec"`
	Width          int    `json:"Width"`
	Height         int    `json:"Height"`
	BitRate        int64  `json:"BitRate"`
	Channels       int    `json:"Channels"`
	ChannelLayout  string `json:"ChannelLayout"`
	VideoRangeType string `json:"VideoRangeType"`
	IsDefault      bool   `json:"IsDefault"`
}
