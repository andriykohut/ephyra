const KIB = 1024;

export function fmtBytes(n: number): string {
  const tb = n / KIB ** 4;
  if (tb >= 1) return `${tb.toFixed(1)} TB`;
  const gb = n / KIB ** 3;
  if (gb >= 1) return `${gb.toFixed(gb >= 100 ? 0 : 1)} GB`;
  return `${(n / KIB ** 2).toFixed(0)} MB`;
}

export function fmtRuntime(sec: number): string {
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  if (d >= 1) return `${d}d ${h}h`;
  const m = Math.floor((sec % 3600) / 60);
  return `${h}h ${m}m`;
}

export function fmtInt(n: number): string {
  return n.toLocaleString("en-US");
}

// Whole hours, no day/minute breakdown -- for the profile header's compact
// stat, where fmtRuntime's "4d 4h" would blow the layout.
export function fmtHours(sec: number): string {
  return `${Math.round(sec / 3600)}h`;
}

// Ranked-row values run from a few seconds to a few hours, unlike fmtRuntime's
// day-scale totals -- "0h 1m" would hide a 100-second play entirely, so this
// drops to minutes:seconds under an hour.
export function fmtDuration(sec: number): string {
  const s = Math.max(0, Math.round(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h >= 1) return `${h}h ${m}m`;
  if (m >= 1) return `${m}m ${s % 60}s`;
  return `${s}s`;
}

// Jellyfin hands us bits per second on the wire — stream BitRate fields and the
// transcoder's target. Not bytes.
export function fmtBitrate(bps: number): string {
  if (bps >= 1_000_000) return `${(bps / 1_000_000).toFixed(1)} Mbps`;
  if (bps >= 1_000) return `${Math.round(bps / 1_000)} kbps`;
  return `${bps} bps`;
}

// "just now" / "6 min ago" / "3 h ago" / "2 d ago"
export function timeAgo(iso: string): string {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 90) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} d ago`;
}
