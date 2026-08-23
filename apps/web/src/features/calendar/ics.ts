import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { Task } from "@crm/contracts";

// ICS uses CRLF, and its text properties have a deliberately small escaping
// grammar. Keeping this here rather than interpolating untrusted task text in
// the page prevents a title containing a newline from becoming a second event
// property when a calendar client imports it.
function escapeText(value: string): string {
  return value.replace(/\\/g, "\\\\").replace(/;/g, "\\;").replace(/,/g, "\\,").replace(/\r?\n/g, "\\n");
}

function localDate(value: Date): string {
  const pad = (part: number) => String(part).padStart(2, "0");
  return `${value.getFullYear()}${pad(value.getMonth() + 1)}${pad(value.getDate())}`;
}

/** Returns a portable all-day calendar event for a task's due date.
 *
 * Tasks currently have a date, not a meeting start/end time. Exporting them
 * as all-day events is therefore honest: Google and Outlook both place them
 * on the selected day without silently inventing a time zone or duration. */
export function taskToIcs(task: Task): string | null {
  if (!task.dueAt) return null;
  const start = timestampDate(task.dueAt);
  const end = new Date(start.getFullYear(), start.getMonth(), start.getDate() + 1);
  const stamp = new Date()
    .toISOString()
    .replace(/[-:]/g, "")
    .replace(/\.\d{3}Z$/, "Z");
  const lines = [
    "BEGIN:VCALENDAR",
    "VERSION:2.0",
    "PRODID:-//Independent CRM//Tasks//EN",
    "CALSCALE:GREGORIAN",
    "BEGIN:VEVENT",
    `UID:task-${task.id}@crm.local`,
    `DTSTAMP:${stamp}`,
    `DTSTART;VALUE=DATE:${localDate(start)}`,
    `DTEND;VALUE=DATE:${localDate(end)}`,
    `SUMMARY:${escapeText(task.title)}`,
    ...(task.description ? [`DESCRIPTION:${escapeText(task.description)}`] : []),
    "END:VEVENT",
    "END:VCALENDAR",
    "",
  ];
  return lines.join("\r\n");
}

export function downloadTaskIcs(task: Task): void {
  const content = taskToIcs(task);
  if (!content) return;
  const blob = new Blob([content], { type: "text/calendar;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = `${
    task.title
      .trim()
      .replace(/[^a-z0-9]+/gi, "-")
      .replace(/^-|-$/g, "") || "task"
  }.ics`;
  link.click();
  URL.revokeObjectURL(url);
}
