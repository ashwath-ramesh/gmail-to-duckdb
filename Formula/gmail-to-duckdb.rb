class GmailToDuckdb < Formula
  desc "Sync Gmail into a local DuckDB file"
  homepage "https://github.com/ashwath-ramesh/gmail-to-duckdb"
  version "0.3.0"

  on_macos do
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.3.0/gmail-to-duckdb-darwin-arm64.tar.gz"
      sha256 "3d850264e5aab915caa15f83d39b36b880de2382e61f67f38a9892ecb2f825ba"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.3.0/gmail-to-duckdb-linux-amd64.tar.gz"
      sha256 "07a2ab643c08b6c1e60c8131513d2f456846a8875013d0daf23563fbd367f869"
    end
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.3.0/gmail-to-duckdb-linux-arm64.tar.gz"
      sha256 "c77c0d4dc1b23be976516108e4632eebf50c9ac09d330fdc2539e44b3817f19d"
    end
  end

  def install
    bin.install "gmail-to-duckdb"
  end

  test do
    assert_match "serve", shell_output("#{bin}/gmail-to-duckdb help")
  end
end
