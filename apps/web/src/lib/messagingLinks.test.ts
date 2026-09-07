import { describe, expect, it } from "vitest";
import {
  buildChannelLink,
  defaultOutreachMessage,
  outreachNote,
  OUTREACH_ACTIVITY_KIND,
} from "./messagingLinks";

/** These are the two failure modes worth a test: a link that opens the wrong
 * conversation, and a message that arrives truncated. Both look fine in code
 * review and fail at the recipient's end. */
describe("buildChannelLink", () => {
  it("strips the + for wa.me, which addresses numbers as bare digits", () => {
    expect(buildChannelLink("whatsapp", "+441632960001", "")).toBe("https://wa.me/441632960001");
  });

  it("keeps the + for tel:, which needs it to dial internationally", () => {
    expect(buildChannelLink("voice", "+441632960001", "ignored")).toBe("tel:+441632960001");
  });

  it("refuses a WhatsApp number that is not digits rather than opening a wrong chat", () => {
    expect(buildChannelLink("whatsapp", "call me", "hi")).toBeNull();
    expect(buildChannelLink("whatsapp", "", "hi")).toBeNull();
  });

  it("encodes characters that would otherwise truncate the message", () => {
    // "&" starts a new query parameter, "#" starts a fragment, and a newline
    // is not legal in a URL at all. Any one of them unescaped means the
    // recipient sees half a sentence.
    const message = "Hi Bob & Sue,\nRe: pricing #2 — 50% off?";
    const link = buildChannelLink("whatsapp", "+441632960001", message)!;

    expect(link.startsWith("https://wa.me/441632960001?text=")).toBe(true);
    expect(link).not.toContain("\n");
    expect(link.slice("https://wa.me/441632960001?text=".length)).not.toContain("&");
    // The real assertion: it round-trips. Anything else is a spelling opinion.
    expect(decodeURIComponent(link.split("?text=")[1])).toBe(message);
  });

  it("round-trips the same message through sms:", () => {
    const message = "Hi Bob & Sue,\n50% off?";
    const link = buildChannelLink("sms", "+441632960001", message)!;
    // "?&body=" rather than "?body=" — the one spelling both iOS and Android
    // honour. See the comment in messagingLinks.ts.
    expect(link.startsWith("sms:+441632960001?&body=")).toBe(true);
    expect(decodeURIComponent(link.split("?&body=")[1])).toBe(message);
  });

  it("round-trips subject and body through mailto:, with real spaces", () => {
    const message = "Hi Bob,\n\nAbout the renewal & the discount.";
    const link = buildChannelLink("email", "bob@example.com", message, "Renewal & pricing")!;
    const query = new URLSearchParams(link.slice(link.indexOf("?") + 1));

    expect(link.startsWith("mailto:bob%40example.com?")).toBe(true);
    expect(query.get("subject")).toBe("Renewal & pricing");
    expect(query.get("body")).toBe(message);
    // URLSearchParams would encode a space as "+", which mail clients render
    // literally — every space in the message would arrive as a plus sign.
    expect(link).not.toContain("+");
  });

  it("omits the query entirely when there is nothing to pre-fill", () => {
    expect(buildChannelLink("email", "bob@example.com", "   ")).toBe("mailto:bob%40example.com");
    expect(buildChannelLink("sms", "+441632960001", "")).toBe("sms:+441632960001");
  });

  it("returns null for a blank address rather than a link to nowhere", () => {
    expect(buildChannelLink("sms", "   ", "hi")).toBeNull();
  });
});

describe("defaultOutreachMessage", () => {
  it("greets by first name", () => {
    expect(defaultOutreachMessage("Bob")).toBe("Hi Bob,");
  });

  it("does not produce 'Hi ,' when the first name is missing", () => {
    // A small failure, and a very visible one to the recipient.
    expect(defaultOutreachMessage("")).toBe("Hi,");
    expect(defaultOutreachMessage("   ")).toBe("Hi,");
  });
});

describe("outreachNote", () => {
  it("says opened, never sent", () => {
    const note = outreachNote("whatsapp", "+441632960001", "Hi Bob,");
    // The honesty of the whole feature is in this word: we know what we
    // handed to the operating system, not that the user pressed send.
    expect(note).toContain("opened");
    expect(note).not.toContain("sent");
    expect(note).toContain("Hi Bob,");
  });

  it("records no message text for a call, because there was none", () => {
    expect(outreachNote("voice", "+441632960001", "Hi Bob,")).toBe("Call opened for +441632960001");
  });

  it("is logged under a kind that does not claim the thing happened", () => {
    expect(OUTREACH_ACTIVITY_KIND).toBe("outreach_attempt");
  });
});
