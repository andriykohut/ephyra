package live

import (
	"fmt"
	"math"
	"net"
	"sort"
	"strings"

	"github.com/andriykohut/ephyra/internal/jellyfin"
)

func normalize(raw []jellyfin.RawSession, server ServerInfo, capacity *int) Snapshot {
	snap := Snapshot{Server: server, Sessions: []Session{}, Summary: Summary{Capacity: capacity}}

	for _, rs := range raw {
		it := rs.NowPlayingItem
		if it == nil || it.MediaType != "Video" || rs.PlayState == nil {
			continue
		}
		vid := firstStream(it.MediaStreams, "Video")
		aud := pickAudio(it.MediaStreams, rs.PlayState.AudioStreamIndex)

		s := Session{
			SessionID:     rs.ID,
			User:          rs.UserName,
			Type:          it.Type,
			Title:         it.Name,
			Series:        it.SeriesName,
			SeasonEpisode: seasonEpisode(it),
			ItemID:        it.ID,
			Art:           art(it),
			PlayMethod:    rs.PlayState.PlayMethod,
			Paused:        rs.PlayState.IsPaused,
			PositionSec:   rs.PlayState.PositionTicks / 10_000_000,
			RuntimeSec:    it.RunTimeTicks / 10_000_000,
			IsRemote:      isRemote(rs.RemoteEndPoint),
			Client:        rs.Client,
			Device:        rs.DeviceName,
			Source:        SourceStreams{Video: videoSource(vid), Audio: audioSource(aud)},
		}
		if s.RuntimeSec > 0 {
			s.ProgressPct = math.Round(float64(s.PositionSec)/float64(s.RuntimeSec)*10000) / 100
		}
		if strings.EqualFold(rs.PlayState.PlayMethod, "Transcode") && rs.TranscodingInfo != nil {
			s.Transcode = transcode(rs.TranscodingInfo, vid, aud, it.Container)
		}
		snap.Sessions = append(snap.Sessions, s)
	}

	sort.Slice(snap.Sessions, func(i, j int) bool {
		return snap.Sessions[i].SessionID < snap.Sessions[j].SessionID
	})

	for _, s := range snap.Sessions {
		snap.Summary.Streams++
		var bps int64
		if s.Transcode != nil {
			snap.Summary.Transcodes++
			bps = s.Transcode.Bitrate
		} else {
			bps = s.Source.Video.Bitrate + s.Source.Audio.Bitrate
		}
		snap.Summary.OutboundBitrate += bps
	}
	return snap
}

func seasonEpisode(it *jellyfin.RawItem) string {
	if it.Type != "Episode" || it.IndexNumber == nil || it.ParentIndexNumber == nil {
		return ""
	}
	return fmt.Sprintf("S%dE%d", *it.ParentIndexNumber, *it.IndexNumber)
}

func art(it *jellyfin.RawItem) Art {
	a := Art{PrimaryTag: it.ImageTags["Primary"]}
	if it.ParentBackdropItemId != "" && len(it.ParentBackdropImageTags) > 0 {
		a.Backdrop = &Backdrop{ItemID: it.ParentBackdropItemId, Tag: it.ParentBackdropImageTags[0]}
	}
	return a
}

func isRemote(ep string) bool {
	host := ep
	if h, _, err := net.SplitHostPort(ep); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}

func firstStream(streams []jellyfin.RawStream, typ string) *jellyfin.RawStream {
	for i := range streams {
		if streams[i].Type == typ {
			return &streams[i]
		}
	}
	return nil
}

func pickAudio(streams []jellyfin.RawStream, idx *int) *jellyfin.RawStream {
	if idx != nil {
		for i := range streams {
			if streams[i].Index == *idx && streams[i].Type == "Audio" {
				return &streams[i]
			}
		}
	}
	for i := range streams {
		if streams[i].Type == "Audio" && streams[i].IsDefault {
			return &streams[i]
		}
	}
	return firstStream(streams, "Audio")
}

func videoSource(s *jellyfin.RawStream) VideoSource {
	if s == nil {
		return VideoSource{}
	}
	return VideoSource{Codec: s.Codec, Width: s.Width, Height: s.Height, Range: s.VideoRangeType, Bitrate: s.BitRate}
}

func audioSource(s *jellyfin.RawStream) AudioSource {
	if s == nil {
		return AudioSource{}
	}
	return AudioSource{Codec: s.Codec, Channels: s.Channels, Layout: s.ChannelLayout, Bitrate: s.BitRate}
}

func transcode(ti *jellyfin.RawTranscodingInfo, vid, aud *jellyfin.RawStream, srcContainer string) *Transcode {
	t := &Transcode{
		Bitrate:       ti.Bitrate,
		CompletionPct: ti.CompletionPercentage,
		HW:            hw(ti.HardwareAccelerationType),
		Reasons:       ti.TranscodeReasons,
	}
	if t.Reasons == nil {
		t.Reasons = []string{}
	}
	if !ti.IsVideoDirect {
		src := ""
		if vid != nil {
			src = vid.Codec
		}
		t.Video = src + "→" + ti.VideoCodec
	}
	if !ti.IsAudioDirect {
		src := ""
		if aud != nil {
			src = aud.Codec
		}
		t.Audio = src + "→" + ti.AudioCodec
	}
	if srcContainer != "" && !strings.EqualFold(srcContainer, ti.Container) {
		t.Container = srcContainer + "→" + ti.Container
	} else {
		t.Container = ti.Container
	}
	return t
}

func hw(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "none" {
		return ""
	}
	return s
}
