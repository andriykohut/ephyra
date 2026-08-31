import { useCallback, useEffect, useRef, useState } from "react";
import { fetchEnvelope } from "@/api/client";
import type { Snapshot } from "@/api/types";

type ConnState = "connecting" | "live" | "reconnecting";

// useNowPlaying keeps a live Now Playing snapshot on screen. It paints once from
// the one-shot endpoint, then hands over to the SSE stream. A "degraded" frame
// carries no sessions, so we keep the last ones visible and just flip the flag.
// Between server frames the position bars are interpolated off a 1s tick so a
// paused-free session doesn't look stuck.
export function useNowPlaying(): { snapshot: Snapshot | null; degraded: boolean; conn: ConnState } {
  const [base, setBase] = useState<Snapshot | null>(null);
  const baseAt = useRef<number>(Date.now());
  const [degraded, setDegraded] = useState(false);
  const [conn, setConn] = useState<ConnState>("connecting");
  const [, forceTick] = useState(0);
  const esRef = useRef<EventSource | null>(null);

  const applySnapshot = useCallback((s: Snapshot) => {
    setBase(s);
    baseAt.current = Date.now();
    setDegraded(s.degraded);
    setConn("live");
  }, []);

  const open = useCallback(() => {
    esRef.current?.close();
    const es = new EventSource("/api/now-playing/stream");
    esRef.current = es;
    const onData = (e: MessageEvent) => applySnapshot(JSON.parse(e.data) as Snapshot);
    es.addEventListener("snapshot", onData);
    es.addEventListener("update", onData);
    es.addEventListener("degraded", () => setDegraded(true));
    es.onerror = () => setConn("reconnecting");
  }, [applySnapshot]);

  // first paint + open the stream
  useEffect(() => {
    let cancelled = false;
    fetchEnvelope<Snapshot>("/api/now-playing")
      .then((env) => {
        if (!cancelled) applySnapshot(env.data);
      })
      .catch(() => setConn("reconnecting"));
    open();
    return () => {
      cancelled = true;
      esRef.current?.close();
      esRef.current = null;
    };
  }, [applySnapshot, open]);

  // visibility: drop the stream when hidden, reopen + refetch when visible
  useEffect(() => {
    const onVis = () => {
      if (document.hidden) {
        esRef.current?.close();
        esRef.current = null;
        setConn("connecting");
      } else {
        fetchEnvelope<Snapshot>("/api/now-playing")
          .then((env) => applySnapshot(env.data))
          .catch(() => {});
        open();
      }
    };
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, [applySnapshot, open]);

  // 1s interpolation tick
  useEffect(() => {
    const id = setInterval(() => forceTick((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, []);

  const snapshot = base && interpolate(base, baseAt.current);
  return { snapshot: snapshot ?? null, degraded, conn };
}

// interpolate advances position_sec / progress_pct for playing sessions by the
// wall time since the last server frame. Paused or runtime-less sessions pass
// through untouched, and nothing runs past the runtime.
function interpolate(s: Snapshot, at: number): Snapshot {
  const dt = (Date.now() - at) / 1000;
  if (dt < 1) return s;
  return {
    ...s,
    sessions: s.sessions.map((sess) => {
      if (sess.paused || sess.runtime_sec <= 0) return sess;
      const pos = Math.min(sess.runtime_sec, sess.position_sec + dt);
      return {
        ...sess,
        position_sec: pos,
        progress_pct: Math.round((pos / sess.runtime_sec) * 10000) / 100,
      };
    }),
  };
}
