import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { fetchEnvelope } from "./client";
import type {
  Cleanup,
  LibraryOverview,
  PlayCursor,
  PlaysPage,
  Profile,
  ProfileList,
  ProfileOverview,
  WatchRange,
  WatchStats,
} from "./types";

export const libraryOverviewQuery = () =>
  queryOptions({
    queryKey: ["library-overview"],
    queryFn: () => fetchEnvelope<LibraryOverview>("/api/library/overview"),
    staleTime: 30 * 60 * 1000,
  });

export const cleanupQuery = (mode: "never" | "stale", sort: "size" | "added") =>
  queryOptions({
    queryKey: ["cleanup", mode, sort],
    queryFn: () => fetchEnvelope<Cleanup>(`/api/cleanup?mode=${mode}&sort=${sort}`),
    staleTime: 30 * 60 * 1000,
  });

export const watchStatsQuery = (range: WatchRange, user: string) =>
  queryOptions({
    queryKey: ["watch-stats", range, user],
    queryFn: () => fetchEnvelope<WatchStats>(`/api/watch/stats?range=${range}&user=${user}`),
    staleTime: 10 * 60 * 1000,
  });

export const profileListQuery = () =>
  queryOptions({
    queryKey: ["profile-list"],
    queryFn: () => fetchEnvelope<ProfileList>("/api/profile"),
    staleTime: 10 * 60 * 1000,
  });

export const profileQuery = (userID: string, range: WatchRange, library: string) =>
  queryOptions({
    queryKey: ["profile", userID, range, library],
    queryFn: () => {
      const params = new URLSearchParams({ range, library });
      return fetchEnvelope<Profile>(`/api/profile/${userID}?${params.toString()}`);
    },
    enabled: userID !== "",
    staleTime: 10 * 60 * 1000,
  });

export const profileOverviewQuery = (userID: string, range: WatchRange, library: string) =>
  queryOptions({
    queryKey: ["profile-overview", userID, range, library],
    queryFn: () => {
      const params = new URLSearchParams({ range, library });
      return fetchEnvelope<ProfileOverview>(`/api/profile/${userID}/overview?${params.toString()}`);
    },
    enabled: userID !== "",
    staleTime: 10 * 60 * 1000,
  });

export const librariesQuery = () =>
  queryOptions({
    queryKey: ["libraries"],
    queryFn: () => fetchEnvelope<string[]>("/api/libraries"),
    staleTime: 10 * 60 * 1000,
  });

// The play history ignores the range selector -- it's a record, not a window
// -- so range is deliberately not part of the key or the request.
export const profilePlaysQuery = (userID: string, library: string) =>
  infiniteQueryOptions({
    queryKey: ["profile-plays", userID, library],
    queryFn: ({ pageParam }) => {
      const params = new URLSearchParams({ limit: "50", library });
      const cur = pageParam as PlayCursor | undefined;
      if (cur) {
        params.set("before", cur.at);
        params.set("before_id", String(cur.row_id));
      }
      return fetchEnvelope<PlaysPage>(`/api/profile/${userID}/plays?${params.toString()}`);
    },
    initialPageParam: undefined as PlayCursor | undefined,
    getNextPageParam: (last) => last.data.next_cursor ?? undefined,
    enabled: userID !== "",
  });
