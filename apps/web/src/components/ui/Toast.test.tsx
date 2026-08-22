import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToastProvider, useToast } from "./Toast";

function Trigger({ duration }: { duration?: number }) {
  const toast = useToast();
  return (
    <button onClick={() => toast.show({ message: "Saved successfully", tone: "success", duration })}>
      Show toast
    </button>
  );
}

describe("Toast", () => {
  it("shows a toast on demand and it is announced via a live region", async () => {
    render(
      <ToastProvider>
        <Trigger duration={0} />
      </ToastProvider>,
    );
    screen.getByText("Show toast").click();
    expect(await screen.findByText("Saved successfully")).toBeInTheDocument();
    expect(screen.getByText("Saved successfully").closest("[role]")).toHaveAttribute("role", "status");
  });

  it("uses an alert role for warning/danger toasts", async () => {
    function DangerTrigger() {
      const toast = useToast();
      return (
        <button onClick={() => toast.show({ message: "Failed", tone: "danger", duration: 0 })}>Go</button>
      );
    }
    render(
      <ToastProvider>
        <DangerTrigger />
      </ToastProvider>,
    );
    screen.getByText("Go").click();
    const el = await screen.findByText("Failed");
    expect(el.closest("[role]")).toHaveAttribute("role", "alert");
  });

  it("dismisses on its own after the given duration", async () => {
    render(
      <ToastProvider>
        <Trigger duration={10} />
      </ToastProvider>,
    );
    screen.getByText("Show toast").click();
    await screen.findByText("Saved successfully");
    await waitFor(() => expect(screen.queryByText("Saved successfully")).not.toBeInTheDocument());
  });

  it("dismisses when the close button is clicked", async () => {
    render(
      <ToastProvider>
        <Trigger duration={0} />
      </ToastProvider>,
    );
    screen.getByText("Show toast").click();
    await screen.findByText("Saved successfully");
    screen.getByLabelText("Dismiss notification").click();
    await waitFor(() => expect(screen.queryByText("Saved successfully")).not.toBeInTheDocument());
  });
});
