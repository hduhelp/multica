import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  __IFRAME_FIND_SHIM__,
  withFindShim,
  FIND_CMD,
  FIND_RESULT,
  FIND_OPEN,
} from "./iframe-find";

describe("withFindShim", () => {
  it("appends the shim verbatim at the end of the original HTML", () => {
    const html = "<p>hello world</p>";
    const out = withFindShim(html);
    expect(out.startsWith(html)).toBe(true);
    expect(out.endsWith(__IFRAME_FIND_SHIM__)).toBe(true);
    expect(out).toBe(html + __IFRAME_FIND_SHIM__);
  });

  it("does not mutate the input string", () => {
    const html = "<p>hi</p>";
    withFindShim(html);
    expect(html).toBe("<p>hi</p>");
  });

  it("handles empty input", () => {
    expect(withFindShim("")).toBe(__IFRAME_FIND_SHIM__);
  });

  it("carries the postMessage protocol tags so parent and iframe agree", () => {
    expect(__IFRAME_FIND_SHIM__).toContain(FIND_CMD);
    expect(__IFRAME_FIND_SHIM__).toContain(FIND_RESULT);
    expect(__IFRAME_FIND_SHIM__).toContain(FIND_OPEN);
  });
});

// The shim ships as a <script> string injected into a srcdoc iframe. To
// exercise its runtime behavior, evaluate the inner script against the current
// jsdom document — close enough to what runs inside the iframe. window.find is
// not implemented in jsdom, so we stub it per-test.
function loadShimIntoDocument() {
  const inner = __IFRAME_FIND_SHIM__
    .replace(/^<script>/, "")
    .replace(/<\/script>$/, "");
  new Function(inner)();
}

describe("find shim runtime behavior", () => {
  let postSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    document.body.innerHTML =
      "<p>alpha beta ALPHA</p><div>alpha gamma</div>"; // 3 case-insensitive "alpha"
    // scrollIntoView isn't implemented in jsdom; the shim guards it but stub it
    // anyway so the select+scroll path runs cleanly.
    Object.defineProperty(window.Element.prototype, "scrollIntoView", {
      configurable: true,
      writable: true,
      value: vi.fn(),
    });
    postSpy = vi.spyOn(window, "postMessage");
    loadShimIntoDocument();
  });

  afterEach(() => {
    document.body.innerHTML = "";
    postSpy.mockRestore();
  });

  function lastResult() {
    for (let i = postSpy.mock.calls.length - 1; i >= 0; i--) {
      const msg = postSpy.mock.calls[i][0] as {
        source?: string;
        found?: boolean;
        total?: number;
        current?: number;
      };
      if (msg && msg.source === FIND_RESULT) return msg;
    }
    return undefined;
  }

  it("counts total case-insensitive matches with a TreeWalker and reports found+current", () => {
    window.dispatchEvent(
      new MessageEvent("message", {
        data: { source: FIND_CMD, action: "search", query: "alpha", caseSensitive: false },
      }),
    );
    const res = lastResult();
    expect(res).toBeDefined();
    expect(res!.total).toBe(3); // alpha, ALPHA, alpha
    expect(res!.found).toBe(true);
    expect(res!.current).toBe(1);
  });

  it("does not count matches inside <script>/<style>/<noscript> text", () => {
    // Regression for the count-inflation bug found in real-browser verification:
    // the TreeWalker used to include the injected shim's own <script> text.
    document.body.innerHTML =
      "<p>alpha</p><script>var s='alpha alpha';</script><style>.alpha{color:red}</style>";
    window.dispatchEvent(
      new MessageEvent("message", {
        data: { source: FIND_CMD, action: "search", query: "alpha", caseSensitive: false },
      }),
    );
    expect(lastResult()!.total).toBe(1); // only the <p>, not script/style text
  });

  it("respects caseSensitive when counting", () => {
    window.dispatchEvent(
      new MessageEvent("message", {
        data: { source: FIND_CMD, action: "search", query: "alpha", caseSensitive: true },
      }),
    );
    expect(lastResult()!.total).toBe(2); // "ALPHA" excluded
  });

  it("reports zero + not-found for a query with no matches", () => {
    window.dispatchEvent(
      new MessageEvent("message", {
        data: { source: FIND_CMD, action: "search", query: "zzz", caseSensitive: false },
      }),
    );
    const res = lastResult()!;
    expect(res.total).toBe(0);
    expect(res.found).toBe(false);
    expect(res.current).toBe(0);
  });

  it("advances the current index on next and wraps at the end", () => {
    const search = (action: string) =>
      window.dispatchEvent(
        new MessageEvent("message", {
          data: { source: FIND_CMD, action, query: "alpha", caseSensitive: false },
        }),
      );
    search("search"); // current=1
    search("next"); // 2
    search("next"); // 3
    expect(lastResult()!.current).toBe(3);
    search("next"); // wraps to 1
    expect(lastResult()!.current).toBe(1);
  });

  it("ignores messages that are not find commands", () => {
    postSpy.mockClear();
    window.dispatchEvent(new MessageEvent("message", { data: { source: "something-else" } }));
    expect(lastResult()).toBeUndefined();
  });

  it("posts an open signal and preventDefaults on Ctrl+F inside the iframe", () => {
    const evt = new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
    window.dispatchEvent(evt);
    expect(evt.defaultPrevented).toBe(true);
    const openMsg = postSpy.mock.calls
      .map((c: unknown[]) => c[0] as { source?: string })
      .find((m: { source?: string }) => m && m.source === FIND_OPEN);
    expect(openMsg).toBeDefined();
  });
});
