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
