import { AVATAR_COLORS, initials } from "../lib/identity";

export default function Avatar({ name, color, size = 24, ring = false }) {
  const bg = AVATAR_COLORS[color] ?? "#786f5f";
  return (
    <span
      title={name}
      aria-label={name}
      className={`inline-flex shrink-0 select-none items-center justify-center rounded-full font-semibold text-white ${
        ring ? "ring-2 ring-white dark:ring-slate-900" : ""
      }`}
      style={{ width: size, height: size, background: bg, fontSize: Math.round(size * 0.42) }}
    >
      {initials(name)}
    </span>
  );
}
