export interface Meta {
  generated_at: string;
  stale: boolean;
}
export interface Envelope<T> {
  data: T;
  meta: Meta;
}

export interface LabeledCount {
  label: string;
  count: number;
}
export interface DiskBucket {
  bucket: string;
  bytes: number;
  items: number;
}
export interface GrowthPoint {
  month: string;
  added_items: number;
  added_bytes: number;
  cum_items: number;
}

export interface LibraryOverview {
  totals: {
    items_by_library: LabeledCount[];
    runtime_seconds: number;
    bytes: number;
    count_uhd: number;
    count_hdr: number;
    count_dv: number;
    series: number;
    items: number;
  };
  disk_by_resolution: DiskBucket[];
  disk_by_codec: DiskBucket[];
  disk_by_container: DiskBucket[];
  disk_by_library: DiskBucket[];
  genres_top: LabeledCount[];
  by_decade: LabeledCount[];
  growth: GrowthPoint[];
  tags: {
    coverage: { tagged: number; total: number };
    top: LabeledCount[];
    pairs: { a: string; b: string; items: number }[];
  };
}

export interface CleanupItem {
  item_id: string;
  scope: "movie" | "series" | "episode";
  name: string;
  library: string;
  bytes: number;
  episodes: number;
  added_at: string;
  last_played_at: string | null;
}
export interface Cleanup {
  mode: "never" | "stale";
  reclaimable_bytes: number;
  match_count: number;
  truncated: boolean;
  items: CleanupItem[];
}

export type WatchRange = "30d" | "90d" | "1y" | "all";

export interface WatchMethodWeek {
  week: string;
  DirectPlay: number;
  Remux: number;
  AudioTranscode: number;
  VideoTranscode: number;
  Other: number;
}
export interface WatchStats {
  plugin_available: boolean;
  range: WatchRange;
  user: string;
  users: { id: string; name: string }[];
  coverage: { first_play: string; last_play: string; total_plays: number };
  totals: {
    watch_seconds: number;
    plays: number;
    active_users: number;
    direct_play_pct: number;
    video_transcode_pct: number;
  };
  top_movies: { item_id: string; name: string; plays: number; watch_sec: number }[];
  top_series: { series_id: string; name: string; plays: number; watch_sec: number }[];
  top_episodes: {
    item_id: string;
    name: string;
    series_name: string;
    plays: number;
    watch_sec: number;
  }[];
  active_users: {
    user_id: string;
    name: string;
    watch_sec: number;
    plays: number;
    distinct_titles: number;
  }[];
  trend: { day: string; watch_sec: number; plays: number }[];
  heatmap: { dow: number; hour: number; watch_sec: number; plays: number }[];
  play_method_weekly: WatchMethodWeek[];
  most_played_core: {
    scope: string;
    item_id: string;
    name: string;
    play_count: number;
    last_played_at: string;
  }[];
}

// --- Now Playing (Plan 3) --- mirrors internal/live JSON tags.

export interface NowVideo {
  codec: string;
  width: number;
  height: number;
  range: string;
  bitrate: number;
}
export interface NowAudio {
  codec: string;
  channels: number;
  layout: string;
  bitrate: number;
}
export interface NowTranscode {
  bitrate: number;
  container: string;
  video: string;
  audio: string;
  hw: string;
  completion_pct: number;
  reasons: string[];
}
export interface NowSession {
  session_id: string;
  user: string;
  type: string;
  title: string;
  series: string;
  season_episode: string;
  item_id: string;
  art: { primary_tag: string; backdrop: { item_id: string; tag: string } | null };
  play_method: string;
  paused: boolean;
  position_sec: number;
  runtime_sec: number;
  progress_pct: number;
  is_remote: boolean;
  client: string;
  device: string;
  source: { video: NowVideo; audio: NowAudio };
  transcode: NowTranscode | null;
}
export interface NowSummary {
  streams: number;
  transcodes: number;
  outbound_bitrate: number;
  capacity: number | null;
}
export interface Snapshot {
  server: { name: string; version: string };
  degraded: boolean;
  summary: NowSummary;
  sessions: NowSession[];
}

// --- Profiles (Plan 4) --- mirrors internal/store profile DTO JSON tags.

export interface ProfileListEntry {
  id: string;
  name: string;
  total_watch_sec: number;
  total_plays: number;
  finished_pct: number;
  rewatch_pct: number;
  longest_binge_episodes: number;
  last_play: string;
}
export interface ProfileList {
  plugin_available: boolean;
  coverage: { first_play: string; last_play: string; total_plays: number };
  users: ProfileListEntry[];
}

export interface TasteEntry {
  key: string;
  watch_sec: number;
  plays: number;
}
export interface Profile {
  range: WatchRange;
  user: { id: string; name: string };
  summary: {
    watch_sec: number;
    plays: number;
    distinct_titles: number;
    days_active: number;
    finished_pct: number;
    bailed_pct: number;
    rewatch_pct: number;
    longest_binge: { episodes: number; series_name: string };
    show_of_range: { series_id: string; series_name: string };
    first_play: string;
    last_play: string;
  };
  completion: { scope: string; bucket: string; count: number }[];
  abandoned: {
    scope: string;
    item_id: string;
    name: string;
    series_name: string;
    bailed_count: number;
  }[];
  rewatch: {
    scope: string;
    item_id: string;
    name: string;
    series_name: string;
    watch_days: number;
  }[];
  binge: {
    series_id: string;
    series_name: string;
    run_episodes: number;
    run_start: string;
    run_end: string;
  }[];
  taste: {
    genre: TasteEntry[];
    decade: TasteEntry[];
    length: TasteEntry[];
    signature_genres: string[];
  };
  baseline: {
    genre: { key: string; watch_sec: number }[];
    decade: { key: string; watch_sec: number }[];
    length: { key: string; watch_sec: number }[];
  };
}
