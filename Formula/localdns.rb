class Localdns < Formula
  desc "Local DNS caching resolver with Redis"
  homepage "https://github.com/yourname/localdns"
  url "https://github.com/yourname/localdns/archive/refs/tags/v0.2.0.tar.gz"
  sha256 "REPLACE_WITH_RELEASE_SHA256"
  license "MIT"

  depends_on "go" => :build
  depends_on "redis"

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w -X github.com/yourname/localdns/internal/app.Version=0.2.0"), "./cmd/localdns"
    bin.install "localdns"
    (etc/"localdns").mkpath
    cp "configs/localdns.yaml", etc/"localdns/localdns.yaml"
  end

  service do
    run [opt_bin/"localdns", "--config", etc/"localdns/localdns.yaml"]
    keep_alive true
    run_at_load true
    require_root true
    error_log_path var/"log/localdns.log"
    working_dir HOMEBREW_PREFIX
  end

  test do
    system bin/"localdns", "--version"
    system bin/"localdns", "--config", etc/"localdns/localdns.yaml", "--check-config"
  end
end
