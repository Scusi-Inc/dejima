# Dejima Homebrew formula (binary).
#
# Copy this into a separate tap repo named `homebrew-dejima` under your account
# (github.com/aoos/homebrew-dejima/Formula/dejima.rb), then:
#   brew install aoos/dejima/dejima
# See docs/distribution.md.
#
# ── Per-release maintenance ────────────────────────────────────────────────
# On each release, bump `version` and replace the four REPLACE_* sha256 values
# with the digests from the release's SHA256SUMS (the release CI can auto-bump
# this file in the tap). Until v0.1.0 is published the sha256s are placeholders,
# so only the HEAD install works:
#   brew install --HEAD aoos/dejima/dejima
#
# Unix archives carry `dejima` + `dejimad`; the daemon only runs on a Unix host.

class Dejima < Formula
  desc "Substrate for multi-device AI agent workflows"
  homepage "https://dejima.tech"
  version "0.1.0"
  license "Pre-public-release" # update to the chosen license before going public

  on_macos do
    on_arm do
      url "https://github.com/aoos/dejima/releases/download/v#{version}/dejima_v#{version}_darwin_arm64.tar.gz"
      sha256 "REPLACE_DARWIN_ARM64_SHA256"
    end
    on_intel do
      url "https://github.com/aoos/dejima/releases/download/v#{version}/dejima_v#{version}_darwin_amd64.tar.gz"
      sha256 "REPLACE_DARWIN_AMD64_SHA256"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/aoos/dejima/releases/download/v#{version}/dejima_v#{version}_linux_arm64.tar.gz"
      sha256 "REPLACE_LINUX_ARM64_SHA256"
    end
    on_intel do
      url "https://github.com/aoos/dejima/releases/download/v#{version}/dejima_v#{version}_linux_amd64.tar.gz"
      sha256 "REPLACE_LINUX_AMD64_SHA256"
    end
  end

  head "https://github.com/aoos/dejima.git", branch: "master"

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      ENV["GOFLAGS"] = "-trimpath"
      system "make", "build"
      bin.install "bin/dejima", "bin/dejimad"
    else
      # Released tarball: client always present; daemon only in Unix archives.
      bin.install "dejima"
      bin.install "dejimad" if File.exist?("dejimad")
    end
  end

  def caveats
    <<~EOS
      Dejima needs a Docker runtime to run the daemon + islands. On macOS:

        brew install --cask docker        # Docker Desktop (recommended)
        brew install --cask orbstack      # or OrbStack (personal-use license)
        brew install colima docker        # or colima (CLI-only, OSS)

      Then build the island image and register the daemon from a checkout:

        make image && dejima service install

      Client-only (a laptop driving a remote daemon)? Skip Docker and just:

        export DEJIMA_HOST=your-host.tailnet:7273
        dejima connect <island>
    EOS
  end

  test do
    assert_match "dejima", shell_output("#{bin}/dejima --version")
  end
end
