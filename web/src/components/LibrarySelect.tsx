export function LibrarySelect({
  value,
  libraries,
  onChange,
}: {
  value: string;
  libraries: string[];
  onChange: (v: string) => void;
}) {
  return (
    <select
      aria-label="library"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="rounded-[10px] border border-line bg-panel/60 px-3 py-1.5 text-[12.5px] text-ink"
    >
      <option value="all">All libraries</option>
      {libraries.map((l) => (
        <option key={l} value={l}>
          {l}
        </option>
      ))}
    </select>
  );
}
