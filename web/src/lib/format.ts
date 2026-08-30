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

// "just now" / "6 min ago" / "3 h ago" / "2 d ago"
export function timeAgo(iso: string): string {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 90) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} d ago`;
}
