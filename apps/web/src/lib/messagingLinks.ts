/** Click-to-chat link construction (task_crm.md §3).
 *
 * The CRM does not send anything. It hands the message to an app the user
 * already has — WhatsApp, the SMS composer, the dialer, the mail client —
 * with the text pre-filled, and the user presses send. That is most of
 * Privyr's core loop with no messaging provider, no per-message cost, no
 * template pre-approval and no 24-hour window, and the message arrives from
 * the rep's own number, which is what the recipient expects.
 *
 * Everything here is a pure function because the two things that break in
 * this feature both break silently: percent-encoding (an unescaped `&` or
 * newline truncates the message, and nobody notices until a customer gets
 * half a sentence) and address formatting (wa.me wants bare digits, `tel:`
 * wants the `+`). Both are pinned by messagingLinks.test.ts.
 */

/** Channel kinds, matching internal/domain/messaging.go's constants. */
export type ChannelKind = "email" | "sms" | "voice" | "whatsapp";

/** The Activity.Kind a click-to-chat hand-off is logged under.
 *
 * Deliberately not "email" or "call": those mean the thing happened. All we
 * know is what we handed to the operating system — the user may never have
 * pressed send. A CRM that recorded an attempt as a completed call would be
 * lying to the person who later reads the timeline, and to whoever reports on
 * it. The notes say which channel and what text. */
export const OUTREACH_ACTIVITY_KIND = "outreach_attempt";

/** Human labels for each channel, for buttons and timeline notes. */
export const CHANNEL_LABELS: Record<ChannelKind, string> = {
  email: "Email",
  sms: "SMS",
  voice: "Call",
  whatsapp: "WhatsApp",
};

/** The verb each channel's button uses. "Call" is not "message". */
export const CHANNEL_ACTIONS: Record<ChannelKind, string> = {
  email: "Email",
  sms: "Text",
  voice: "Call",
  whatsapp: "WhatsApp",
};

/** Builds the URL that opens the user's own app with `message` pre-filled.
 *
 * `address` must already be normalised by the server (lower-cased email, or a
 * phone number in `+<digits>` form) — ListContactChannels returns it that way,
 * and a channel it marked unreachable should never reach this function.
 * Returns null rather than a broken link if it does. */
export function buildChannelLink(
  kind: ChannelKind,
  address: string,
  message: string,
  subject?: string,
): string | null {
  const trimmedAddress = address.trim();
  if (!trimmedAddress) return null;
  const text = message.trim();

  switch (kind) {
    case "email": {
      // Both parts are query parameters, so both need encoding; a subject
      // containing "&" would otherwise start a bogus parameter and lose the
      // body entirely.
      const params = new URLSearchParams();
      if (subject) params.set("subject", subject);
      if (text) params.set("body", text);
      const query = params.toString();
      // URLSearchParams encodes a space as "+", which is correct for form
      // bodies and wrong in a mailto: query — mail clients render the plus
      // literally, so every space in the message becomes a "+" character.
      const mailtoQuery = query.replace(/\+/g, "%20");
      return `mailto:${encodeURIComponent(trimmedAddress)}${mailtoQuery ? `?${mailtoQuery}` : ""}`;
    }
    case "voice":
      // No message: a dialer has nowhere to put one.
      return `tel:${trimmedAddress}`;
    case "sms": {
      if (!text) return `sms:${trimmedAddress}`;
      // "?&body=" rather than "?body=" is not a typo. RFC 5724 says "?body=",
      // and Android honours it, but iOS's Messages app historically only
      // picks the body up after an ampersand; "?&" is the one spelling both
      // platforms accept. Needs a real-device check on any iOS or Android
      // version bump — this is empirical, not specified.
      return `sms:${trimmedAddress}?&body=${encodeURIComponent(text)}`;
    }
    case "whatsapp": {
      // wa.me addresses a number as bare digits. Leaving the "+" on produces
      // a link WhatsApp either rejects or resolves to a different number, and
      // it fails at the recipient's end rather than ours — so it is the kind
      // of bug that ships.
      const digits = trimmedAddress.replace(/^\+/, "");
      if (!/^\d+$/.test(digits)) return null;
      return `https://wa.me/${digits}${text ? `?text=${encodeURIComponent(text)}` : ""}`;
    }
    default:
      return null;
  }
}

/** The default message offered before the user edits it.
 *
 * Deliberately just a greeting. A CRM that pre-writes a paragraph of
 * enthusiasm produces messages that read like a CRM wrote them, which is the
 * opposite of what a one-to-one channel is for. There is no sign-off because
 * there is nothing truthful to sign with: the access token carries `sub` and
 * `email`, not a display name, and a name derived from an email local part
 * would be a guess printed under the user's own message. They can type it —
 * and templates (task_crm.md §5) will carry the tenant's own wording. */
export function defaultOutreachMessage(firstName: string): string {
  return firstName.trim() ? `Hi ${firstName.trim()},` : "Hi,";
}

/** The timeline note for one hand-off.
 *
 * Says "opened", never "sent". The whole honesty of the feature is in this
 * string: we know what we handed to the operating system, not what the user
 * did next. */
export function outreachNote(kind: ChannelKind, address: string, message: string): string {
  const header = `${CHANNEL_LABELS[kind]} opened for ${address}`;
  const text = message.trim();
  if (kind === "voice" || !text) return header;
  return `${header}\n\n${text}`;
}
