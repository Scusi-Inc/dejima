// Stamp the navbar status with the latest published release tag, e.g.
// "v0.5.3 · alpha". Falls back to a bare "alpha" if the fetch fails or is
// rate-limited — it never blocks render. Zero upkeep: always current.
fetch('https://api.github.com/repos/aoos/dejima/releases/latest')
  .then(function (r) { return r.ok ? r.json() : null; })
  .then(function (d) {
    if (!d || !d.tag_name) return;
    document.querySelectorAll('.nav-ver').forEach(function (el) {
      el.textContent = d.tag_name + ' · ';
    });
  })
  .catch(function () {});
