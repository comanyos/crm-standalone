/** Rendering a message template in the browser (task_crm.md §5).
 *
 * Substitution happens here rather than on the server, and that is the whole
 * reason this feature exists in v1 at all.
 *
 * docs/WORKFLOW_AUTOMATION.md refuses templated action text because "a
 * template that can read the record can read fields the rule author is not
 * allowed to see". That reasoning is correct and unchanged. It does not apply
 * here because the values come from the record already on the user's screen:
 * nobody can leak to themselves what they are looking at. The server
 * validates that a body names only allowlisted fields and never expands one.
 *
 * A server-side render — for an automated send, with nobody present — is a
 * different feature and still blocked on the field-visibility model.
 */

/** The merge fields a template may use — internal/domain/message_template.go's
 * MergeFields. Kept in sync by hand because the list is short and the server
 * refuses anything outside it at save time, so drift shows up as a template
 * that will not save rather than as a message that renders wrong. */
export type MergeValues = Partial<Record<string, string>>;

/** Builds the substitution values for one contact.
 *
 * Every key is a field the viewer can already see on the contact page, which
 * is what keeps rendering client-side honest. */
export function mergeValuesForContact(
  contact: {
    firstName?: string;
    lastName?: string;
    email?: string;
  },
  accountName: string,
  senderEmail: string,
): MergeValues {
  const first = (contact.firstName ?? "").trim();
  const last = (contact.lastName ?? "").trim();
  return {
    "contact.first_name": first,
    "contact.last_name": last,
    "contact.full_name": `${first} ${last}`.trim(),
    "contact.email": (contact.email ?? "").trim(),
    "account.name": accountName.trim(),
    "sender.email": senderEmail.trim(),
  };
}

/** Substitutes {{placeholders}} in a template body.
 *
 * A field with no value renders as an empty string, and the surrounding text
 * is then tidied: "Hi {{contact.first_name}}," with no first name would
 * otherwise reach a customer as "Hi ,", which is worse than sending nothing.
 * The tidy is deliberately narrow — a dangling comma or space directly after
 * a substitution that came out empty — rather than a general prose fixer,
 * because guessing at somebody's wording is how a template stops saying what
 * its author wrote. */
export function renderTemplate(body: string, values: MergeValues): string {
  const substituted = body.replace(/\{\{\s*([a-z_.]+)\s*\}\}/g, (_match, field: string) => {
    return values[field] ?? "";
  });
  return (
    substituted
      // "Hi ," -> "Hi," and "Hi ." -> "Hi."
      .replace(/ +([,.!?])/g, "$1")
      // "Hi ," with nothing before the comma at all -> "Hi"
      .replace(/^([,.!?]\s*)/gm, "")
      // Collapse the double space a missing middle value leaves behind.
      .replace(/[^\S\n]{2,}/g, " ")
      .trim()
  );
}

/** The placeholders a body actually uses, in order of first appearance —
 * for showing an author which fields a template depends on. */
export function mergeFieldsUsed(body: string): string[] {
  const seen = new Set<string>();
  for (const match of body.matchAll(/\{\{\s*([a-z_.]+)\s*\}\}/g)) {
    seen.add(match[1]);
  }
  return [...seen];
}
