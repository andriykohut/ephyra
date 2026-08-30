import { cn } from "@/lib/utils";

export function SegmentedControl<T extends string>({
  value,
  options,
  onChange,
  ariaLabel,
}: {
  value: T;
  options: { value: T; label: string }[];
  onChange: (v: T) => void;
  ariaLabel: string;
}) {
  return (
    <fieldset className="m-0 inline-flex min-w-0 rounded-[10px] border border-line bg-panel/60 p-0.5">
      <legend className="sr-only">{ariaLabel}</legend>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={o.value === value}
          onClick={() => onChange(o.value)}
          className={cn(
            "rounded-[8px] px-3 py-1.5 text-[12.5px] font-medium transition-colors",
            o.value === value ? "bg-cyan/15 text-cyan" : "text-muted hover:text-ink",
          )}
        >
          {o.label}
        </button>
      ))}
    </fieldset>
  );
}
