class Localdns < Formula
  desc "Local DNS caching resolver with Redis"
  homepage "https://github.com/bithubakkount/dns"
  url "https://github.com/bithubakkount/dns/archive/refs/tags/v0.3.3.tar.gz"
  sha256 "REPLACE_AFTER_TAGGING"
  license "MIT"

  depends_on "go" => :build
  depends_on "redis"

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w -X github.com/bithubakkount/dns/internal/app.Version=0.3.3"), "./cmd/localdns"

    bin.install "localdns"

    (etc/"localdns").mkpath
    cp "configs/localdns.yaml", etc/"localdns/localdns.yaml"
  end

  service do
    run [opt_bin/"localdns", "--config", etc/"localdns/localdns.yaml"]
    keep_alive true
    run_at_load true
    require_root true
    process_type :background
    error_log_path var/"log/localdns.error.log"
    log_path var/"log/localdns.log"
    working_dir HOMEBREW_PREFIX
  end

  test do
    assert_equal "0.3.3", shell_output("#{bin}/localdns --version").strip
    system bin/"localdns", "--config", etc/"localdns/localdns.yaml", "--check-config"
  end
end
