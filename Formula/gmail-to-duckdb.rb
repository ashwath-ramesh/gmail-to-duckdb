class GmailToDuckdb < Formula
  desc "Sync Gmail into a local DuckDB file"
  homepage "https://github.com/ashwath-ramesh/gmail-to-duckdb"
  version "0.2.0"

  on_macos do
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.2.0/gmail-to-duckdb-darwin-arm64.tar.gz"
      sha256 "82f14799ad6e908e227b6445ec043396fbf1cedbda82e9b418c3e1077060483a"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.2.0/gmail-to-duckdb-linux-amd64.tar.gz"
      sha256 "63174a676cf20b9bc807b6d2b78a632f05dea516c1114f8b6e784b08b688a4a7"
    end
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.2.0/gmail-to-duckdb-linux-arm64.tar.gz"
      sha256 "755e2a50b0f1036eec74d119664c5bf048bfa86418452b8a4cb4a3e801d0c79f"
    end
  end

  def install
    bin.install "gmail-to-duckdb"
  end

  test do
    assert_match "serve", shell_output("#{bin}/gmail-to-duckdb help")
  end
end
