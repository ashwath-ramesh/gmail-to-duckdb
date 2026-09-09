class GmailToDuckdb < Formula
  desc "Sync Gmail into a local DuckDB file"
  homepage "https://github.com/ashwath-ramesh/gmail-to-duckdb"
  version "0.1.2"

  on_macos do
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.1.2/gmail-to-duckdb-darwin-arm64.tar.gz"
      sha256 "9b6c6aa2a6b0e1c22eca97dea7f4feda01b254cb0827b2e65b8636c0f91759b4"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.1.2/gmail-to-duckdb-linux-amd64.tar.gz"
      sha256 "3e402c31eae8cd9e490dfc6d1b0abf1972d4b3f7dfca17aa7ca03587e24dbda6"
    end
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.1.2/gmail-to-duckdb-linux-arm64.tar.gz"
      sha256 "52d39d21c7de20e1e1c38bb003208a352e65ff10fdca8faeae68a97c0c6836ec"
    end
  end

  def install
    bin.install "gmail-to-duckdb"
  end

  test do
    assert_match "serve", shell_output("#{bin}/gmail-to-duckdb help")
  end
end
