import { Link } from "@tanstack/react-router";
import { Panel } from "@/components/Panel";

export function NotFound() {
  return (
    <div className="flex flex-col gap-6">
      <h1 className="font-display text-[clamp(24px,4vw,32px)] font-bold tracking-[-0.03em] text-ink">
        Not found
      </h1>
      <Panel className="p-5">
        <p className="text-[13px] leading-relaxed text-muted">
          That page isn't one of Ephyra's. There are four: Library, Watch Stats, Now Playing,
          Cleanup.
        </p>
        <Link
          to="/library"
          className="mt-3 inline-block rounded-[10px] border border-line px-3 py-1.5 text-[13px] text-ink hover:border-cyan hover:text-cyan"
        >
          Go to Library
        </Link>
      </Panel>
    </div>
  );
}
