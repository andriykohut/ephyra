export function StaleBanner({ onRefresh }: { onRefresh?: () => void }) {
  return (
    <div
      role="status"
      className="flex items-center gap-3 rounded-[14px] border border-line/70 border-l-[3px] border-l-violet/70 bg-panel/60 px-4 py-3 text-[13px] text-muted backdrop-blur-xl"
    >
      <span>Showing the last good refresh. The next one is running.</span>
      {onRefresh && (
        <button
          type="button"
          onClick={onRefresh}
          className="ml-auto rounded-[10px] border border-line px-3 py-1.5 text-[12px] font-medium text-ink hover:border-cyan hover:text-cyan"
        >
          Refresh now
        </button>
      )}
    </div>
  );
}
