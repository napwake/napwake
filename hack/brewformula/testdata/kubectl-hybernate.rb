# Written by the release workflow of https://github.com/okedeji/hybernate
# with its hack/brewformula, for each release: change that, not this.
class KubectlHybernate < Formula
  desc "Find idle Kubernetes workloads, and pause and wake them with Hybernate"
  homepage "https://github.com/okedeji/hybernate"
  version "0.2.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://github.com/okedeji/hybernate/releases/download/v0.2.0/kubectl-hybernate-darwin-arm64.tar.gz"
      sha256 "1a349b12b50ad5b43740e0952adc33c7805ce06f091074be977624d09ed9d432"
    end
    on_intel do
      url "https://github.com/okedeji/hybernate/releases/download/v0.2.0/kubectl-hybernate-darwin-amd64.tar.gz"
      sha256 "5fc18e80e71334e0ced23b751a1b626ad0a806e097ba20202032db5e05923e3f"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/okedeji/hybernate/releases/download/v0.2.0/kubectl-hybernate-linux-arm64.tar.gz"
      sha256 "17d46d4991b2edd5e445342a72ba0cb7cf09e4849b5e98c16408ce11e05c7388"
    end
    on_intel do
      url "https://github.com/okedeji/hybernate/releases/download/v0.2.0/kubectl-hybernate-linux-amd64.tar.gz"
      sha256 "abb35c616421af72198ad7c2aeeef38516f08f6a7afb2a728cf0068a8a712ddc"
    end
  end

  def install
    bin.install Dir["kubectl-hybernate-*"].first => "kubectl-hybernate"
  end

  test do
    assert_match "kubectl-hybernate v#{version}", shell_output("#{bin}/kubectl-hybernate version")
  end
end
