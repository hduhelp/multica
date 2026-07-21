/**
 * In-page find shim for sandboxed HTML attachment iframes (#5259).
 *
 * HTML attachment previews mount user HTML inside a
 * `<iframe sandbox="allow-scripts" srcdoc={...}>` WITHOUT `allow-same-origin`
 * (see code-block-iframe.tsx / attachment-preview-page.tsx). That opaque-origin
 * sandbox is deliberate — it isolates untrusted uploads — but it has a side
 * effect: the browser's native Ctrl+F / Cmd+F find-in-page cannot search into
 * an opaque-origin iframe, and the parent document cannot reach the iframe's
 * DOM across the origin boundary either. So maximizing an HTML preview left the
 * user with no way to search a large document.
 *
 * The fix mirrors withFragmentNavShim: inject a tiny script into the srcdoc that
 * runs in the iframe's own opaque origin (same capability `allow-scripts`
 * already grants) and does the search on its own document, driven by the parent
 * over postMessage. It adds NO new capability and does NOT relax the sandbox.
 *
 * Protocol:
 *   parent -> iframe: { source: FIND_CMD, action: "search"|"next"|"prev"|"clear",
 *                       query, caseSensitive }
 *   iframe -> parent: { source: FIND_RESULT, found, total, current }
 *                     { source: FIND_OPEN }   // Ctrl/Cmd+F pressed inside iframe
 *
 * Total match count is computed with a TreeWalker (deterministic); stepping uses
 * window.find (non-standard but stable on the target Chromium/WebKit platforms —
 * web Chrome + desktop Electron — and gives native find-like select/scroll). The
 * `current` index is tracked as a wrapping counter; a standards-based
 * CSS-Custom-Highlight rewrite can replace window.find later without touching
 * this protocol.
 */

/** postMessage `source` tag for parent -> iframe commands. */
export const FIND_CMD = "multica-find-cmd";
/** postMessage `source` tag for iframe -> parent result updates. */
export const FIND_RESULT = "multica-find-result";
/** postMessage `source` tag for iframe -> parent "open the find bar" signal. */
export const FIND_OPEN = "multica-find-open";

const FIND_SHIM = `<script>
(function(){
  var CMD=${JSON.stringify(FIND_CMD)};
  var RESULT=${JSON.stringify(FIND_RESULT)};
  var OPEN=${JSON.stringify(FIND_OPEN)};

  // We deliberately do NOT use window.find(): it operates on the *focused*
  // window, but the find bar lives in the parent, so the sandboxed iframe never
  // holds focus and window.find returns false inside it (confirmed in Chromium).
  // Instead we collect matches as Ranges via a TreeWalker and select+scroll them
  // with the Selection API, which is focus-independent and standard.
  var matches = [];   // [{ node, start, end }]
  var idx = -1;       // 0-based index into matches, -1 when none
  var lastQuery = null;
  var lastCase = false;

  function collect(query, caseSensitive){
    matches = [];
    lastQuery = query;
    lastCase = caseSensitive;
    if(!query) return;
    var root = document.body || document.documentElement;
    if(!root) return;
    // Skip <script>/<style>/<noscript> text — including THIS injected shim's own
    // <script>, which would otherwise inflate the count when the user searches
    // for a word that appears in the shim source (e.g. "search").
    var walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
      acceptNode: function(node){
        var tag = node.parentNode && node.parentNode.nodeName;
        if(tag === "SCRIPT" || tag === "STYLE" || tag === "NOSCRIPT") return NodeFilter.FILTER_REJECT;
        return NodeFilter.FILTER_ACCEPT;
      }
    });
    var needle = caseSensitive ? query : query.toLowerCase();
    var node;
    while((node = walker.nextNode())){
      var raw = node.nodeValue || "";
      var hay = caseSensitive ? raw : raw.toLowerCase();
      var i = 0;
      while((i = hay.indexOf(needle, i)) !== -1){
        matches.push({ node: node, start: i, end: i + needle.length });
        i += needle.length;
      }
    }
  }

  function clearSelection(){
    var s = window.getSelection && window.getSelection();
    if(s && s.removeAllRanges){ try { s.removeAllRanges(); } catch(_){} }
  }

  function highlightCurrent(){
    clearSelection();
    if(idx < 0 || idx >= matches.length) return;
    var m = matches[idx];
    try {
      var range = document.createRange();
      range.setStart(m.node, m.start);
      range.setEnd(m.node, m.end);
      var sel = window.getSelection && window.getSelection();
      if(sel && sel.addRange) sel.addRange(range);
      var el = m.node.parentElement ||
        (m.node.parentNode && m.node.parentNode.nodeType === 1 ? m.node.parentNode : null);
      if(el && el.scrollIntoView) el.scrollIntoView({ block: "center", inline: "nearest" });
    } catch(_){}
  }

  function post(){
    try {
      parent.postMessage({
        source: RESULT,
        found: idx >= 0,
        total: matches.length,
        current: idx >= 0 ? idx + 1 : 0
      }, "*");
    } catch(_){}
  }

  // Rebuild the match list only when the query or case flag changed.
  function ensure(query, caseSensitive){
    if(query === lastQuery && caseSensitive === lastCase) return false;
    collect(query, caseSensitive);
    idx = matches.length ? 0 : -1;
    return true;
  }

  function doSearch(query, caseSensitive){
    ensure(query, caseSensitive);
    idx = matches.length ? 0 : -1;
    highlightCurrent();
    post();
  }

  function step(query, caseSensitive, backwards){
    var rebuilt = ensure(query, caseSensitive);
    if(!matches.length){ idx = -1; post(); return; }
    if(!rebuilt){
      idx = backwards
        ? (idx <= 0 ? matches.length - 1 : idx - 1)
        : (idx >= matches.length - 1 ? 0 : idx + 1);
    }
    highlightCurrent();
    post();
  }

  window.addEventListener("message", function(e){
    var d = e && e.data;
    if(!d || d.source !== CMD) return;
    if(d.action === "search") doSearch(d.query || "", !!d.caseSensitive);
    else if(d.action === "next") step(d.query || "", !!d.caseSensitive, false);
    else if(d.action === "prev") step(d.query || "", !!d.caseSensitive, true);
    else if(d.action === "clear"){ matches = []; idx = -1; lastQuery = null; clearSelection(); }
  });

  window.addEventListener("keydown", function(e){
    if((e.ctrlKey || e.metaKey) && (e.key === "f" || e.key === "F")){
      e.preventDefault();
      try { parent.postMessage({ source: OPEN }, "*"); } catch(_){}
    }
  });
})();
</script>`;

/**
 * Appends the find shim to an HTML document string destined for a sandboxed
 * srcdoc iframe. Compose with withFragmentNavShim, e.g.
 * `withFindShim(withFragmentNavShim(text))`.
 */
export function withFindShim(html: string | undefined): string {
  return (html ?? "") + FIND_SHIM;
}

/** Exposed for unit tests so they can assert the shim was appended verbatim. */
export const __IFRAME_FIND_SHIM__ = FIND_SHIM;
