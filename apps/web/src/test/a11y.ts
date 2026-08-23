import axe, { type Result, type RuleObject } from "axe-core";
import { expect } from "vitest";

/** Runs axe over a rendered container and fails with the violations spelled
 * out, rather than a bare boolean.
 *
 * **What this tier can and cannot see.** jsdom has no layout engine and
 * applies no stylesheet, so every rule that depends on how something is
 * painted is off below — colour contrast, target size, whether an element is
 * actually obscured. What is left is the half that lives in the markup: roles,
 * accessible names, label associations, landmark structure, heading order,
 * duplicate ids, ARIA attributes that contradict their element. That is the
 * half most defects are in, and the half a component test can hold onto.
 *
 * The other half needs a real browser, and the roadmap still owes it
 * (Lighthouse/axe against the running app, plus visual regression). Passing
 * here is not a WCAG claim; it means nothing in the markup is obviously
 * wrong.
 */
const LAYOUT_DEPENDENT_RULES: RuleObject = {
  // Needs computed colours from a stylesheet jsdom never loaded — it would
  // report "incomplete" on every node forever.
  "color-contrast": { enabled: false },
  // Needs box geometry.
  "target-size": { enabled: false },
};

export async function expectNoA11yViolations(container: HTMLElement) {
  const results = await axe.run(container, {
    rules: LAYOUT_DEPENDENT_RULES,
    // "incomplete" results are things axe could not decide without a browser.
    // Reporting them here would be noise, so only definite violations fail.
    resultTypes: ["violations"],
  });
  expect(describe(results.violations)).toBe("");
}

function describe(violations: Result[]) {
  return violations
    .map((violation) => {
      const where = violation.nodes.map((node) => `      ${node.html}`).join("\n");
      return `  [${violation.impact}] ${violation.id}: ${violation.help}\n${where}`;
    })
    .join("\n");
}
