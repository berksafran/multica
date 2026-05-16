import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render } from "@testing-library/react";

// TerminalPanel only renders in the Desktop Electron runtime. The check is
// `"desktopAPI" in window`; setting any value on the property is enough.
beforeEach(() => {
  (window as unknown as Record<string, unknown>).desktopAPI = {};
});
afterEach(() => {
  delete (window as unknown as Record<string, unknown>).desktopAPI;
  vi.unstubAllGlobals();
  vi.resetModules();
});

// xterm needs DOM APIs jsdom doesn't ship and pulls in canvas-flavored
// rendering paths. Mock both at the @xterm scope so the panel can mount
// without touching the real implementation; we only care about the WS
// handshake frames.
vi.mock("@xterm/xterm", () => {
  class FakeTerminal {
    public cols = 80;
    public rows = 24;
    loadAddon() {}
    open() {}
    write() {}
    writeln() {}
    onData() {
      return { dispose: () => {} };
    }
    onResize() {
      return { dispose: () => {} };
    }
    dispose() {}
  }
  return { Terminal: FakeTerminal };
});
vi.mock("@xterm/addon-fit", () => {
  class FitAddon {
    fit() {}
  }
  return { FitAddon };
});
vi.mock("@xterm/xterm/css/xterm.css", () => ({}));

// The panel reads the bearer token via getApi().getToken(). In the
// Desktop runtime (token mode) that returns a non-empty string; cookie
// mode returns null.
const apiMock = vi.hoisted(() => ({
  getToken: vi.fn<() => string | null>(() => "test-bearer-token"),
  getBaseUrl: vi.fn<() => string>(() => "http://api.test.local"),
}));
vi.mock("@multica/core/api", () => ({
  getApi: () => apiMock,
}));

// Capture every WebSocket instance the panel constructs so the test can
// drive the handshake and assert on outbound frames.
interface FakeWS {
  url: string;
  readyState: number;
  sent: string[];
  onopen: ((ev?: unknown) => void) | null;
  onmessage: ((ev: { data: string }) => void) | null;
  onerror: ((ev?: unknown) => void) | null;
  onclose: ((ev: { code: number; reason: string }) => void) | null;
  send: (data: string) => void;
  close: () => void;
}
function installFakeWS(): { instances: FakeWS[] } {
  const instances: FakeWS[] = [];
  class FakeWebSocket implements FakeWS {
    static OPEN = 1;
    static CONNECTING = 0;
    static CLOSING = 2;
    static CLOSED = 3;
    public url: string;
    public readyState = FakeWebSocket.CONNECTING;
    public sent: string[] = [];
    public onopen: ((ev?: unknown) => void) | null = null;
    public onmessage: ((ev: { data: string }) => void) | null = null;
    public onerror: ((ev?: unknown) => void) | null = null;
    public onclose: ((ev: { code: number; reason: string }) => void) | null =
      null;
    constructor(url: string) {
      this.url = url;
      instances.push(this);
    }
    send(data: string) {
      this.sent.push(data);
    }
    close() {
      this.readyState = FakeWebSocket.CLOSED;
      this.onclose?.({ code: 1000, reason: "" });
    }
  }
  vi.stubGlobal("WebSocket", FakeWebSocket);
  return { instances };
}

// jsdom doesn't ship ResizeObserver; the panel registers one on its
// container. The shared setup.ts already provides a stub, but assert
// existence so this test is self-contained.
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver =
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
}

describe("TerminalPanel token-mode first-frame auth", () => {
  it("sends `auth` frame on open when getToken() returns a bearer", async () => {
    apiMock.getToken.mockReturnValue("test-bearer-token");
    const { instances } = installFakeWS();

    const { TerminalPanel } = await import("./terminal-panel");
    render(<TerminalPanel issueId="issue-1" workspaceId="ws-1" />);

    expect(instances).toHaveLength(1);
    const ws = instances[0]!;

    // Simulate the browser's open event. Before the fix, this transitioned
    // to "connected" without sending anything, and the server's 10s
    // first-frame auth deadline closed the connection.
    ws.readyState = 1;
    ws.onopen?.();

    expect(ws.sent.length).toBeGreaterThanOrEqual(1);
    const first = JSON.parse(ws.sent[0]!) as {
      type: string;
      payload: { token: string };
    };
    expect(first.type).toBe("auth");
    expect(first.payload.token).toBe("test-bearer-token");
  });

  it("skips the `auth` frame in cookie mode (getToken returns null)", async () => {
    apiMock.getToken.mockReturnValue(null);
    const { instances } = installFakeWS();

    const { TerminalPanel } = await import("./terminal-panel");
    render(<TerminalPanel issueId="issue-1" workspaceId="ws-1" />);

    const ws = instances[0]!;
    ws.readyState = 1;
    ws.onopen?.();

    // Cookie auth is already resolved by the time onopen fires — the panel
    // must NOT pre-emptively send any frame, otherwise it would race with
    // the server's own terminal.open flow.
    expect(ws.sent).toEqual([]);
  });
});
