import { render } from "@testing-library/react";
import { describe, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { EmptyState, ErrorState, ForbiddenState, NotFoundState, UnavailableState } from "./StatePanel";
import { Button } from "./Button";
import { expectNoA11yViolations } from "../../test/a11y";

/** Every screen falls back to one of these, and they are the states nobody
 * looks at twice: a reader hits them exactly when something has already gone
 * wrong, which is the worst moment to also be unable to find the retry. */
describe("StatePanel accessibility", () => {
  it("has no violations in the empty state, with and without an action", async () => {
    const plain = render(<EmptyState title="No accounts" description="Create one to begin." />);
    await expectNoA11yViolations(plain.container);
    plain.unmount();

    const withAction = render(
      <EmptyState title="No accounts" description="Create one to begin." action={<Button>New account</Button>} />,
    );
    await expectNoA11yViolations(withAction.container);
  });

  it("has no violations in the error state, with and without a retry", async () => {
    const plain = render(<ErrorState description="Something went wrong." />);
    await expectNoA11yViolations(plain.container);
    plain.unmount();

    const retryable = render(<ErrorState description="Something went wrong." onRetry={vi.fn()} />);
    await expectNoA11yViolations(retryable.container);
  });

  it("has no violations in the forbidden state", async () => {
    const { container } = render(<ForbiddenState description="You don't have permission to view accounts." />);
    await expectNoA11yViolations(container);
  });

  it("has no violations in the not-found state", async () => {
    // Rendered inside a router: this one can offer a link back.
    const { container } = render(
      <MemoryRouter>
        <NotFoundState description="That account no longer exists." />
      </MemoryRouter>,
    );
    await expectNoA11yViolations(container);
  });

  it("has no violations in the unavailable state", async () => {
    const { container } = render(<UnavailableState onRetry={vi.fn()} />);
    await expectNoA11yViolations(container);
  });
});
