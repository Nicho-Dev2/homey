// Small helpers for showing numbers and dates.

export function formatSize(bytes: number): string {
  if (!bytes) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let n = bytes;
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n >= 10 || i === 0 ? Math.round(n) : n.toFixed(1)} ${units[i]}`;
}

// formatAge turns a date into "now", "12 min", "5 h", "3 d", "2 mo", "4 y".
export function formatAge(iso?: string): string {
  if (!iso) return "—";
  const ms = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(ms)) return "—";
  const min = ms / 60_000;
  if (min < 1) return "now";
  if (min < 60) return `${Math.floor(min)} min`;
  const h = min / 60;
  if (h < 24) return `${Math.floor(h)} h`;
  const d = h / 24;
  if (d < 60) return `${Math.floor(d)} d`;
  if (d < 730) return `${Math.floor(d / 30)} mo`;
  return `${Math.floor(d / 365)} y`;
}

const groups: Record<number, string> = {
  1000: "Console", 2000: "Movies", 3000: "Audio", 4000: "PC", 5000: "TV",
  6000: "XXX", 7000: "Books", 8000: "Other",
};

// categoryLabel names the main group of the first category: 2040 → "Movies".
export function categoryLabel(cats: number[]): string {
  if (!cats.length) return "";
  return groups[Math.floor(cats[0] / 1000) * 1000] ?? "";
}
