import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export interface Column<Row> {
  key: string;
  header: string;
  cell: (r: Row) => ReactNode;
  align?: "left" | "right";
}

export function DataTable<Row>({
  columns,
  rows,
  getKey,
  empty,
}: {
  columns: Column<Row>[];
  rows: Row[];
  getKey: (r: Row) => string;
  empty?: ReactNode;
}) {
  if (rows.length === 0 && empty) {
    return <div className="px-4 py-8 text-center text-[13px] text-muted">{empty}</div>;
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-[13px]">
        <thead>
          <tr className="border-line border-b text-[11px] uppercase tracking-[0.12em] text-muted">
            {columns.map((c) => (
              <th
                key={c.key}
                className={cn(
                  "px-3 py-2 font-semibold",
                  c.align === "right" ? "text-right" : "text-left",
                )}
              >
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={getKey(r)} className="border-line/50 border-b last:border-0">
              {columns.map((c) => (
                <td
                  key={c.key}
                  className={cn(
                    "px-3 py-2.5 tabular-nums text-ink",
                    c.align === "right" ? "text-right" : "text-left",
                  )}
                >
                  {c.cell(r)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
