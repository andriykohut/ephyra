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
