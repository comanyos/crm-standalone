import { describe, expect, it } from "vitest";
import { mergeFieldsUsed, mergeValuesForContact, renderTemplate } from "./messageTemplates";

const values = mergeValuesForContact(
  { firstName: "Bob", lastName: "Buyer", email: "bob@example.com" },
  "Acme Holdings",
  "rep@example.com",
);

describe("renderTemplate", () => {
  it("substitutes every allowlisted field", () => {
    expect(
      renderTemplate("Hi {{contact.first_name}}, this is {{sender.email}} about {{account.name}}.", values),
    ).toBe("Hi Bob, this is rep@example.com about Acme Holdings.");
  });

  it("tolerates spacing inside the braces, as authors type it", () => {
    expect(renderTemplate("Hi {{ contact.first_name }},", values)).toBe("Hi Bob,");
  });

  it("does not send 'Hi ,' when a value is missing", () => {
    // The failure this exists for: a contact with no first name would
    // otherwise get a message opening "Hi ,". Very visible, entirely
    // avoidable, and the kind of thing nobody notices until a customer does.
    const empty = mergeValuesForContact({ firstName: "", lastName: "" }, "", "");
    expect(renderTemplate("Hi {{contact.first_name}},", empty)).toBe("Hi,");
  });

  it("collapses the gap a missing middle value leaves", () => {
    const partial = mergeValuesForContact({ firstName: "Bob", lastName: "" }, "", "rep@example.com");
    expect(
      renderTemplate("Hi {{contact.first_name}} {{contact.last_name}}, from {{sender.email}}.", partial),
    ).toBe("Hi Bob, from rep@example.com.");
  });

  it("leaves an unknown placeholder empty rather than printing braces", () => {
    // The server refuses these at save time, so this only matters for a
    // template stored before a field was retired. Literal "{{...}}" reaching
    // a customer is the worse of the two outcomes.
    expect(renderTemplate("Hi {{contact.nickname}}!", values)).toBe("Hi!");
  });

  it("preserves deliberate line breaks", () => {
    expect(renderTemplate("Hi {{contact.first_name}},\n\nAbout the renewal.", values)).toBe(
      "Hi Bob,\n\nAbout the renewal.",
    );
  });

  it("passes a body with no placeholders through unchanged", () => {
    expect(renderTemplate("Just checking in.", values)).toBe("Just checking in.");
  });
});

describe("mergeValuesForContact", () => {
  it("builds a full name from the parts, without a stray space", () => {
    expect(mergeValuesForContact({ firstName: "Bob", lastName: "" }, "", "")["contact.full_name"]).toBe(
      "Bob",
    );
    expect(mergeValuesForContact({ firstName: "", lastName: "Buyer" }, "", "")["contact.full_name"]).toBe(
      "Buyer",
    );
    expect(mergeValuesForContact({}, "", "")["contact.full_name"]).toBe("");
  });
});

describe("mergeFieldsUsed", () => {
  it("lists each field once, so an author can see what a template depends on", () => {
    expect(mergeFieldsUsed("Hi {{contact.first_name}}, {{contact.first_name}} at {{account.name}}")).toEqual([
      "contact.first_name",
      "account.name",
    ]);
    expect(mergeFieldsUsed("No fields here")).toEqual([]);
  });
});
