class GmailToDuckdb < Formula
  desc "Sync Gmail into a local DuckDB file"
  homepage "https://github.com/ashwath-ramesh/gmail-to-duckdb"
  version "0.4.1"

  on_macos do
    on_intel do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.4.1/gmail-to-duckdb-darwin-amd64.tar.gz"
      sha256 "e47222e2c41aaaebaf407a32949dd8331d0d0c560801f4a1881993d3b3973089"
    end
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.4.1/gmail-to-duckdb-darwin-arm64.tar.gz"
      sha256 "638cc37aa0d0d61b66a0ca533462a9841343716d756d0f5cdea8d73ee9d73070"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.4.1/gmail-to-duckdb-linux-amd64.tar.gz"
      sha256 "6a8f12625dcfc48a5fad549bf380150a4bac9b37a2e7d8a40353e043a2757b31"
    end
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.4.1/gmail-to-duckdb-linux-arm64.tar.gz"
      sha256 "edcf564bf02eaf026d3293e6054c04e9e5241901c27b0c83c317e89b784470bf"
    end
  end

  def install
    bin.install "gmail-to-duckdb"
  end

  test do
    assert_match "serve", shell_output("#{bin}/gmail-to-duckdb help")
  end
end
