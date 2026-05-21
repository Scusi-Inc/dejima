# Dejima Homebrew formula.
#
# This file lives in the dejima/ repo as a reference. To make `brew install`
# work, copy it into a separate tap repository named `homebrew-dejima` under
# your GitHub account (e.g. github.com/aoos/homebrew-dejima/Formula/dejima.rb).
# See docs/distribution.md for the bootstrap steps.
#
# Until v0.1.0 is tagged + released, the formula only supports head installs:
#   brew install --HEAD aoos/dejima/dejima
#
# After a release exists, add `url` and `sha256` lines so users can
# `brew install aoos/dejima/dejima` against the tagged version.

class Dejima < Formula
  desc "Substrate for multi-device AI agent workflows"
  homepage "https://dejima.dev"
  license "Pre-public-release" # update to "Apache-2.0" (or chosen license) before going public
  head "https://github.com/aoos/dejima.git", branch: "master"

  depends_on "go" => :build

  def install
    ENV["GOFLAGS"] = "-trimpath"
    system "make", "build"
    bin.install "bin/dejima"
    bin.install "bin/dejimad"
  end

  def caveats
    <<~EOS
      Dejima requires a Docker runtime. On macOS, Docker Desktop is the
      recommended default (free for small business use):

        brew install --cask docker

      Faster alternatives if you'd rather install one yourself:
        brew install --cask orbstack    # personal-use license only
        brew install colima docker      # CLI-only, OSS

      Then build the island image and register the daemon:

        cd "#{HOMEBREW_PREFIX}" && \\
          curl -fsSL https://raw.githubusercontent.com/aoos/dejima/master/scripts/setup.sh \\
            | SKIP_BUILD=1 bash

      Or simply: `dejima service install` (after building the island image
      via `make image` from a checkout).
    EOS
  end

  test do
    assert_match "dejima", shell_output("#{bin}/dejima --version")
    assert_match(/.+/, shell_output("#{bin}/dejimad --version"))
  end
end
