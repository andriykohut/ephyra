import { queryOptions } from "@tanstack/react-query";
import { fetchEnvelope } from "./client";
import type {
  Cleanup,
  LibraryOverview,
  Profile,
  ProfileList,
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

export const profileQuery = (userID: string, range: WatchRange) =>
  queryOptions({
    queryKey: ["profile", userID, range],
    queryFn: () => fetchEnvelope<Profile>(`/api/profile/${userID}?range=${range}`),
    enabled: userID !== "",
    staleTime: 10 * 60 * 1000,
  });
