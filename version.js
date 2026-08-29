// Stamp the navbar status with the latest published release tag, e.g.
// "v0.5.3 · alpha". Falls back to a bare "alpha" if the fetch fails or is
// rate-limited — it never blocks render. The tag is zero upkeep; the owner/repo
// below is not, and because the failure is silent by design, a stale one shows
// as a missing version rather than an error. Update it if the repo moves.
fetch('https://api.github.com/repos/Scusi-Inc/dejima/releases/latest')
  .then(function (r) { return r.ok ? r.json() : null; })
  .then(function (d) {
    if (!d || !d.tag_name) return;
    document.querySelectorAll('.nav-ver').forEach(function (el) {
      el.textContent = d.tag_name + ' · ';
    });
  })
  .catch(function () {});
