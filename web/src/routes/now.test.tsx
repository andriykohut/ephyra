import { render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { NowSession, Snapshot } from "@/api/types";
import { NowPlayingView } from "./now";

const sess = (over: Partial<NowSession> = {}): NowSession => ({
  session_id: "a",
  user: "alice",
  type: "Episode",
  title: "The Long Retreat",
  series: "Northwind",
  season_episode: "S1E7",
  item_id: "item1",
  art: { primary_tag: "p1", backdrop: { item_id: "b1", tag: "t1" } },
  play_method: "DirectPlay",
  paused: false,
  position_sec: 2522,
  runtime_sec: 3360,
  progress_pct: 75.1,
  is_remote: false,
  client: "Jellyfin Media Player",
  device: "Living Room Mac",
  source: {
    video: { codec: "hevc", width: 1920, height: 1080, range: "SDR", bitrate: 4_417_693 },
    audio: { codec: "aac", channels: 6, layout: "5.1", bitrate: 480_014 },
  },
  transcode: null,
  ...over,
});

const snap = (over: Partial<Snapshot> = {}): Snapshot => ({
  server: { name: "jellyfin.example.lan", version: "10.11.11" },
  degraded: false,
  summary: { streams: 1, transcodes: 0, outbound_bitrate: 4_897_707, capacity: null },
  sessions: [sess()],
  ...over,
});

afterEach(() => vi.restoreAllMocks());

test("renders a session card with progress and quality chips", () => {
  render(<NowPlayingView snapshot={snap()} degraded={false} conn="live" />);
  expect(screen.getByText("The Long Retreat")).toBeInTheDocument();
  expect(screen.getByText(/Northwind/)).toBeInTheDocument();
  expect(screen.getByText(/S1E7/)).toBeInTheDocument();
  expect(screen.getByText(/HEVC/i)).toBeInTheDocument();
  expect(screen.getByText(/1080p/)).toBeInTheDocument();
  expect(screen.getByText(/local/i)).toBeInTheDocument();
  const poster = screen.getByRole("img", { name: /poster/i });
  expect(poster).toHaveAttribute("src", expect.stringContaining("/api/now-playing/art/item1"));
});

test("transcode session shows the arrow rows and raw reasons", () => {
  render(
    <NowPlayingView
      snapshot={snap({
        summary: { streams: 1, transcodes: 1, outbound_bitrate: 9_200_000, capacity: 6 },
        sessions: [
          sess({
            play_method: "Transcode",
            transcode: {
              bitrate: 9_200_000,
              container: "mkv→ts",
              video: "hevc→h264",
              audio: "",
              hw: "qsv",
              completion_pct: 41.3,
              reasons: ["VideoCodecNotSupported"],
            },
          }),
        ],
      })}
      degraded={false}
      conn="live"
    />,
  );
  expect(screen.getByText("hevc→h264")).toBeInTheDocument();
  expect(screen.queryByText(/audio/i)).not.toBeNull(); // label present
  expect(screen.getByText("VideoCodecNotSupported")).toBeInTheDocument();
  expect(screen.getByText(/1 transcoding/)).toBeInTheDocument();
  expect(screen.getByText(/\/ 6/)).toBeInTheDocument(); // capacity gauge
});

test("empty and degraded states", () => {
  const { rerender } = render(
    <NowPlayingView snapshot={snap({ sessions: [] })} degraded={false} conn="live" />,
  );
  expect(screen.getByText(/nothing playing/i)).toBeInTheDocument();
  rerender(
    <NowPlayingView
      snapshot={snap({ sessions: [], degraded: true })}
      degraded={true}
      conn="reconnecting"
    />,
  );
  expect(screen.getByText(/can't reach jellyfin/i)).toBeInTheDocument();
});
