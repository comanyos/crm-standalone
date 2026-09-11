import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// Node's BroadcastChannel crosses Vitest worker boundaries, while a browser
// channel is isolated by origin. A per-test-environment implementation keeps
// independently running spec files from resetting one another's idle timers.
const broadcastGroups = new Map<string, Set<InMemoryBroadcastChannel>>();

class InMemoryBroadcastChannel extends EventTarget {
  readonly name: string;
  onmessage: ((this: BroadcastChannel, event: MessageEvent) => unknown) | null = null;
  onmessageerror: ((this: BroadcastChannel, event: MessageEvent) => unknown) | null = null;
  private closed = false;

  constructor(name: string) {
    super();
    this.name = name;
    const group = broadcastGroups.get(name) ?? new Set<InMemoryBroadcastChannel>();
    group.add(this);
    broadcastGroups.set(name, group);
  }

  postMessage(message: unknown) {
    if (this.closed) throw new DOMException("BroadcastChannel is closed", "InvalidStateError");
    const recipients = [...(broadcastGroups.get(this.name) ?? [])].filter((peer) => peer !== this);
    void Promise.resolve().then(() => {
      for (const recipient of recipients) {
        if (recipient.closed) continue;
        const event = new MessageEvent("message", { data: message });
        recipient.dispatchEvent(event);
        recipient.onmessage?.call(recipient as unknown as BroadcastChannel, event);
      }
    });
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    const group = broadcastGroups.get(this.name);
    group?.delete(this);
    if (group?.size === 0) broadcastGroups.delete(this.name);
  }
}

Object.defineProperty(globalThis, "BroadcastChannel", {
  configurable: true,
  writable: true,
  value: InMemoryBroadcastChannel,
});

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
