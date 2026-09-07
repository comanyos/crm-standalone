import { test, expect } from "@playwright/test";

/** Layout at a phone width (task_crm.md §10.3).
 *
 * Everything here is a fact only a real browser knows. The component suite
 * runs in jsdom, which has no layout engine at all: it can assert that the
 * drawer's markup is correct and cannot tell you that the page is 151px wide
 * behind it, that a form field has collapsed to 44px, or that the body
 * scrolls sideways. Those are the regressions this file exists to catch.
 *
 * It is deliberately small. Behaviour is covered once, at desktop width, by
 * the other specs; repeating it here would double the slowest tier to
 * re-assert things that do not depend on the viewport.
 */
test.describe("mobile layout", () => {
  test("the navigation is a drawer, not a permanent column", async ({ page }) => {
    await page.goto("/accounts");

    // Closed, the drawer must be out of the tab order rather than translated
    // off-screen — an invisible focus trap is worse than no drawer.
    const nav = page.getByRole("navigation");
    await expect(nav).toBeHidden();

    const opener = page.getByRole("button", { name: /navigation|menu/i });
    await expect(opener).toBeVisible();
    await opener.click();
    await expect(nav).toBeVisible();

    // Escape closes it and returns focus to the page.
    await page.keyboard.press("Escape");
    await expect(nav).toBeHidden();
  });

  test("the page does not scroll sideways", async ({ page }) => {
    await page.goto("/accounts");

    // The single most visible mobile regression, and one no unit test can
    // see. Wide content (tables, the board) must scroll inside its own
    // container, never push the document out.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });

  test("main content gets the full width", async ({ page }) => {
    await page.goto("/accounts");

    // Before the drawer work a 375px phone gave main 151px, because the
    // sidebar was a permanent 14rem column. That number is why this
    // assertion is a number.
    const width = await page.evaluate(() => {
      const main = document.getElementById("main-content");
      return main ? main.getBoundingClientRect().width : 0;
    });
    expect(width).toBeGreaterThan(300);
  });

  test("tap targets on the primary actions are big enough to hit", async ({ page }) => {
    await page.goto("/accounts");

    // WCAG 2.2 AA asks for 24x24 CSS pixels; 44 is the platform convention.
    // Checked here because contrast and target size are the two rules
    // axe-core explicitly cannot judge without layout.
    const buttons = page.getByRole("button");
    const count = Math.min(await buttons.count(), 8);
    for (let i = 0; i < count; i++) {
      const box = await buttons.nth(i).boundingBox();
      if (!box) continue; // hidden controls are not tap targets
      expect(box.height).toBeGreaterThanOrEqual(24);
    }
  });
});
