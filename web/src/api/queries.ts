import { queryOptions } from "@tanstack/react-query";
import { fetchEnvelope } from "./client";
import type { Cleanup, LibraryOverview } from "./types";

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
