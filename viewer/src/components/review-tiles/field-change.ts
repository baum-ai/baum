export const fieldChange = {
  add: { label: "Added", tone: "positive", background: "bg-(--status-positive-bg)" },
  remove: { label: "Removed", tone: "critical", background: "bg-(--status-critical-bg)" },
  alter: { label: "Changed", tone: "caution", background: "bg-(--status-caution-bg)" },
  rename: { label: "Renamed", tone: "info", background: "bg-(--status-info-bg)" },
} as const;
