import { describe, expect, it } from "vitest";
import { dateToZonedLocal, zonedLocalToDate } from "./zonedDateTime";

describe("zoned date-time conversion", () => {
  it("creates the requested wall-clock time outside the browser zone", () => {
    expect(zonedLocalToDate("2026-09-14T09:00", "Asia/Kolkata").toISOString()).toBe(
      "2026-09-14T03:30:00.000Z",
    );
  });

  it("uses the daylight-saving offset for the selected date", () => {
    expect(zonedLocalToDate("2026-07-10T09:00", "America/New_York").toISOString()).toBe(
      "2026-07-10T13:00:00.000Z",
    );
  });

  it("round-trips an instant into a datetime-local field", () => {
    expect(dateToZonedLocal(new Date("2026-09-14T03:30:00Z"), "Asia/Kolkata")).toBe(
      "2026-09-14T09:00",
    );
  });

  it("rejects unknown zones", () => {
    expect(() => zonedLocalToDate("2026-09-14T09:00", "Mars/Olympus")).toThrow("IANA");
  });
});
