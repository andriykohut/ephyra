import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { Snapshot } from "@/api/types";
import { useNowPlaying } from "./useNowPlaying";

class FakeES {
  static last: FakeES | null = null;
  url: string;
  readyState = 1; // OPEN
  onerror: ((e: unknown) => void) | null = null;
  listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
  closed = false;
  constructor(url: string) {
    this.url = url;
    FakeES.last = this;
  }
  addEventListener(t: string, fn: (e: MessageEvent) => void) {
    this.listeners[t] ??= [];
    this.listeners[t].push(fn);
  }
  emit(t: string, data: unknown) {
    for (const fn of this.listeners[t] ?? []) fn({ data: JSON.stringify(data) } as MessageEvent);
  }
  close() {
    this.closed = true;
    this.readyState = 2; // CLOSED
  }
}

// the tests assert these exist before reaching for them; the casts keep the
// bodies free of non-null assertions.
const es = () => FakeES.last as FakeES;
const sessions = (s: Snapshot | null) => (s as Snapshot).sessions;

const snap = (over: Partial<Snapshot> = {}): Snapshot => ({
  server: { name: "S", version: "10.11.11" },
  degraded: false,
  summary: { streams: 1, transcodes: 0, outbound_bitrate: 5e6, capacity: null },
  sessions: [
    {
      session_id: "a",
      user: "u",
      type: "Movie",
      title: "T",
      series: "",
      season_episode: "",
      item_id: "i",
      art: { primary_tag: "", backdrop: null },
      play_method: "DirectPlay",
      paused: false,
      position_sec: 100,
      runtime_sec: 6000,
      progress_pct: 1.67,
      is_remote: false,
      client: "c",
      device: "d",
      source: {
        video: { codec: "h264", width: 1920, height: 1080, range: "SDR", bitrate: 5e6 },
        audio: { codec: "aac", channels: 2, layout: "stereo", bitrate: 2e5 },
      },
      transcode: null,
    },
  ],
  ...over,
});

beforeEach(() => {
  vi.stubGlobal("EventSource", FakeES as unknown as typeof EventSource);
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify({ data: snap(), meta: { generated_at: "x", stale: false } }), {
      status: 200,
    }),
  );
});
afterEach(() => {
  Object.defineProperty(document, "hidden", { value: false, configurable: true });
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

test("first paint from the one-shot, then EventSource takes over", async () => {
  const { result } = renderHook(() => useNowPlaying());
  await waitFor(() => expect(result.current.snapshot?.sessions[0].title).toBe("T"));
  expect(FakeES.last?.url).toContain("/api/now-playing/stream");

  act(() =>
    es().emit(
      "update",
      snap({ summary: { streams: 2, transcodes: 1, outbound_bitrate: 9e6, capacity: null } }),
    ),
  );
  expect(result.current.snapshot?.summary.streams).toBe(2);
  expect(result.current.conn).toBe("live");
});

test("degraded event sets the flag; a later update clears it", async () => {
  const { result } = renderHook(() => useNowPlaying());
  await waitFor(() => expect(result.current.snapshot).not.toBeNull());
  act(() => es().emit("degraded", { degraded: true }));
  expect(result.current.degraded).toBe(true);
  // the whole point of the degraded frame: what was on screen stays on screen
  expect(sessions(result.current.snapshot)).toHaveLength(1);
  expect(sessions(result.current.snapshot)[0].title).toBe("T");
  act(() => es().emit("update", snap()));
  expect(result.current.degraded).toBe(false);
});

test("interpolates position for non-paused sessions", async () => {
  vi.useFakeTimers();
  const { result } = renderHook(() => useNowPlaying());
  // waitFor deadlocks under fake timers (its own poll never ticks), so flush the
  // one-shot fetch by hand instead.
  await act(async () => {
    await vi.runOnlyPendingTimersAsync();
  });
  expect(result.current.snapshot).not.toBeNull();
  const p0 = sessions(result.current.snapshot)[0].position_sec;
  act(() => vi.advanceTimersByTime(3000));
  expect(sessions(result.current.snapshot)[0].position_sec).toBeGreaterThanOrEqual(p0 + 2);
});

test("paused sessions do not advance, and the advance stops at runtime_sec", async () => {
  vi.useFakeTimers();
  const { result } = renderHook(() => useNowPlaying());
  await act(async () => {
    await vi.runOnlyPendingTimersAsync();
  });

  const base = snap();
  const [only] = base.sessions;
  act(() =>
    es().emit("update", {
      ...base,
      sessions: [
        { ...only, session_id: "paused", paused: true, position_sec: 100 },
        { ...only, session_id: "ending", position_sec: 5998, runtime_sec: 6000 },
      ],
    }),
  );
  act(() => vi.advanceTimersByTime(10_000));

  const [paused, ending] = sessions(result.current.snapshot);
  expect(paused.position_sec).toBe(100);
  expect(ending.position_sec).toBe(6000);
  expect(ending.progress_pct).toBeLessThanOrEqual(100);
});

test("closes the EventSource when the tab is hidden", async () => {
  const { result } = renderHook(() => useNowPlaying());
  await waitFor(() => expect(result.current.snapshot).not.toBeNull());
  const opened = es();
  act(() => {
    Object.defineProperty(document, "hidden", { value: true, configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  expect(opened.closed).toBe(true);
});

test("an EventSource error flips conn to reconnecting; a later frame restores it", async () => {
  const { result } = renderHook(() => useNowPlaying());
  await waitFor(() => expect(result.current.snapshot).not.toBeNull());
  expect(result.current.conn).toBe("live");

  act(() => FakeES.last?.onerror?.(new Event("error")));
  expect(result.current.conn).toBe("reconnecting");

  act(() => es().emit("update", snap()));
  expect(result.current.conn).toBe("live");
});
