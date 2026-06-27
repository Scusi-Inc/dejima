// Wire up "have an AI walk you through it" blocks on guide pages.
//
// Each block is a .walkthrough containing a textarea[data-prompt] (the prompt,
// pre-filled in the HTML so it works without JS), a [data-copy] button, and
// optional [data-claude] / [data-chatgpt] deep links. We sync the (possibly
// user-edited) prompt into the chat URLs via ?q= prefill, and wire the copy
// button. No network call of our own; the prompt only goes where the user sends
// it. Claude and ChatGPT support ?q= on the web; Gemini doesn't, so copy stays
// the universal fallback.
(function () {
  document.querySelectorAll('.walkthrough').forEach(function (root) {
    var ta = root.querySelector('[data-prompt]');
    if (!ta) return;
    var btn = root.querySelector('[data-copy]');
    var claude = root.querySelector('[data-claude]');
    var chatgpt = root.querySelector('[data-chatgpt]');

    function sync() {
      var q = encodeURIComponent(ta.value);
      if (claude) claude.href = 'https://claude.ai/new?q=' + q;
      if (chatgpt) chatgpt.href = 'https://chatgpt.com/?q=' + q;
    }
    ta.addEventListener('input', sync);
    sync();

    if (btn) {
      btn.addEventListener('click', function () {
        var done = function () {
          var prev = btn.textContent;
          btn.textContent = 'Copied!';
          setTimeout(function () { btn.textContent = prev; }, 1600);
        };
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(ta.value).then(done, function () {
            ta.select(); document.execCommand('copy'); done();
          });
        } else {
          ta.select(); document.execCommand('copy'); done();
        }
      });
    }
  });
})();
