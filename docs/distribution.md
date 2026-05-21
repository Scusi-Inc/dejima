# Distribution Setup

How to wire up the two short-install paths. These are one-time admin steps you take outside the repo.

---

## Path A — GitHub Pages (gets you `aoos.github.io/dejima` right away)

### 1. Enable GitHub Pages on `aoos/dejima`

In the repo settings on GitHub:

- **Settings → Pages**
- **Source**: Deploy from a branch
- **Branch**: `master` → folder `/(root)`
- Save.

(GitHub Pages only allows `/` or `/docs` as serving folders. The site files live at the repo root: `index.html`, `install.sh`. `.nojekyll` disables Jekyll processing so the other root files like `README.md` aren't rendered.)

That's it. Within a minute, the install URL is live:

```bash
curl -fsSL https://aoos.github.io/dejima/install.sh | bash
```

The landing page is at `https://aoos.github.io/dejima/`.

### 2. Verify

```bash
curl -fsSL https://aoos.github.io/dejima/install.sh | head -5
```

Should print the script's shebang and a few lines of comments.

### Cost / effort

Free. Five minutes. **This is the recommended first step.**

---

## Path B — Custom short domain (e.g. `dejima.sh/install.sh`)

If you want a brand-pure URL, add a custom domain on top of Path A.

### 1. Register the domain

`dejima.dev` is squatted ($1,488 aftermarket). Realistic alternatives:

| Domain | Typical /yr | Notes |
|--------|---|---|
| `dejima.sh` | ~$32 | CLI-tool convention. Strongest brand fit. |
| `dejima.app` | ~$14 | Clean, on-brand, broadly available. |
| `dejima.io` | ~$45-60 | Classic dev TLD; check for premium pricing. |
| `dejima.cc` | ~$12 | Cheap, unusual, fine. |
| `getdejima.com` | ~$10 | Common product-site pattern. |

Recommended registrar: **Cloudflare Registrar** for at-cost pricing.

### 2. Add CNAME to the repo

Create `web/CNAME` containing exactly your domain (no protocol, no trailing slash):

```
dejima.sh
```

Commit + push.

### 3. Point DNS at GitHub Pages

In your DNS provider's UI, add these records for the apex (`@`):

```
TYPE   NAME    VALUE
A      @       185.199.108.153
A      @       185.199.109.153
A      @       185.199.110.153
A      @       185.199.111.153
AAAA   @       2606:50c0:8000::153
AAAA   @       2606:50c0:8001::153
AAAA   @       2606:50c0:8002::153
AAAA   @       2606:50c0:8003::153
```

(Re-verify the canonical IPs at [docs.github.com/en/pages](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site/managing-a-custom-domain-for-your-github-pages-site#configuring-an-apex-domain) before adding — GitHub updates them occasionally.)

DNS propagation: usually minutes, occasionally hours.

### 4. Update the install URL in the README

Replace `https://aoos.github.io/dejima/install.sh` with `https://<your-domain>/install.sh`. That's the only string change needed.

### 5. Verify

```bash
curl -fsSL https://<your-domain>/install.sh | head -5
```

### Cost / effort

$10–$45 / year depending on TLD, ~30 minutes of setup.

---

## Path B — `brew install aoos/dejima/dejima`

### 1. Create the tap repository

On GitHub, create a new public repo named **exactly** `homebrew-dejima` under your account (so the full path is `github.com/aoos/homebrew-dejima`). Homebrew discovers taps via this naming convention.

### 2. Add the formula

Copy `homebrew/dejima.rb` from this repo into the new tap repo at:

```
homebrew-dejima/
  Formula/
    dejima.rb
```

Commit and push.

### 3. Install

Until you tag a release, the formula only supports head installs:

```bash
brew install --HEAD aoos/dejima/dejima
```

Once you tag `v0.1.0` and push GitHub Releases binaries (or a source tarball), update `homebrew/dejima.rb` here, copy the changes to `homebrew-dejima/Formula/dejima.rb`, and users can drop `--HEAD`:

```bash
brew install aoos/dejima/dejima
```

### 4. (Optional later) Submit to homebrew-core

After Dejima has real users and a stable release cadence, submit to [homebrew/homebrew-core](https://github.com/Homebrew/homebrew-core) for the cleanest possible `brew install dejima`. This is months of stewardship work and gated on Homebrew's acceptance criteria — defer until v1.x is real.

### Cost / effort

Free, ~1 hour for the tap repo + formula validation.

---

## Recommended order

1. **Enable GitHub Pages on the repo** → `curl aoos.github.io/dejima/install.sh | bash` works (~5 min, free). **Do this first.**
2. **Set up the Homebrew tap** → `brew install --HEAD aoos/dejima/dejima` works (~1 hour, free).
3. **Pick + register a custom domain** → e.g. `curl dejima.sh/install.sh | bash` (~30 min, ~$32/yr).
4. **GitHub Releases (CI tag-driven binary builds)** → `brew install aoos/dejima/dejima` is fast and Go-free (few hours, free).
5. **Eventually**: submit to homebrew-core for `brew install dejima`.
