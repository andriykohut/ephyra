import { cn } from "@/lib/utils";
import { Panel } from "./Panel";

export function StatCard({
  label,
  value,
  mote = false,
}: {
  label: string;
  value: string;
  mote?: boolean;
}) {
  return (
    <Panel
      className={cn(
        "px-4 py-3.5",
        mote && "border-mote/40 shadow-[0_0_26px_-10px_rgba(255,194,75,0.45)]",
      )}
    >
      <div
        className={cn(
          "text-[10.5px] font-semibold uppercase tracking-[0.12em]",
          mote ? "text-mote/80" : "text-muted",
        )}
      >
        {label}
      </div>
      <div
        className={cn(
          "mt-1.5 whitespace-nowrap font-mono text-[19px] font-semibold tabular-nums tracking-tight",
          mote ? "text-mote" : "text-ink",
        )}
      >
        {value}
      </div>
    </Panel>
  );
}
