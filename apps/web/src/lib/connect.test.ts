import { describe, expect, it } from "vitest";
import { createTraceParent } from "./connect";

describe("createTraceParent", () => {
  it("creates an opaque valid W3C traceparent", () => {
    expect(createTraceParent()).toMatch(/^00-[0-9a-f]{32}-[0-9a-f]{16}-01$/);
  });
});
