import { describe, expect, it } from "vitest";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { makeTask } from "../../test/harness";
import { taskToIcs } from "./ics";

describe("taskToIcs", () => {
  it("exports an all-day event that calendar clients can safely import", () => {
    const ics = taskToIcs(
      makeTask({
        id: "task-1",
        title: "Review, proposal; notes",
        description: "Line one\nLine two",
        dueAt: timestampFromDate(new Date(2026, 7, 23, 23, 59, 59)),
      }),
    );
    expect(ics).toContain("BEGIN:VCALENDAR\r\n");
    expect(ics).toContain("UID:task-task-1@crm.local");
    expect(ics).toContain("DTSTART;VALUE=DATE:20260823");
    expect(ics).toContain("DTEND;VALUE=DATE:20260824");
    expect(ics).toContain("SUMMARY:Review\\, proposal\\; notes");
    expect(ics).toContain("DESCRIPTION:Line one\\nLine two");
  });

  it("does not export an undated task as a made-up calendar event", () => {
    expect(taskToIcs(makeTask({ dueAt: undefined }))).toBeNull();
  });
});
