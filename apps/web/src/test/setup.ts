import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// jsdom implements <dialog> as an element but not its modal behaviour, so
// showModal/close throw. Our Dialog (components/ui/Dialog.tsx) is built on
// the native element deliberately — for focus trapping and Escape handling
// the browser does better than hand-rolled JS — so the shim exists purely to
// let jsdom render it. It reproduces only what the tests observe: `open`
// toggling, and the `close` event.
if (typeof HTMLDialogElement !== "undefined" && !HTMLDialogElement.prototype.showModal) {
  HTMLDialogElement.prototype.showModal = function showModal(this: HTMLDialogElement) {
    this.open = true;
  };
  HTMLDialogElement.prototype.show = function show(this: HTMLDialogElement) {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) {
    this.open = false;
    this.dispatchEvent(new Event("close"));
  };
}

afterEach(() => cleanup());
