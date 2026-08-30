export function UserSelect({
  value,
  users,
  onChange,
}: {
  value: string;
  users: { id: string; name: string }[];
  onChange: (v: string) => void;
}) {
  return (
    <select
      aria-label="user"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="rounded-[10px] border border-line bg-panel/60 px-3 py-1.5 text-[12.5px] text-ink"
    >
      <option value="all">All users</option>
      {users.map((u) => (
        <option key={u.id} value={u.id}>
          {u.name}
        </option>
      ))}
    </select>
  );
}
