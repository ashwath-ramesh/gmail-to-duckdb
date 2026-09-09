class GmailToDuckdb < Formula
  desc "Sync Gmail into a local DuckDB file"
  homepage "https://github.com/ashwath-ramesh/gmail-to-duckdb"
  version "0.1.1"

  on_macos do
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.1.1/gmail-to-duckdb-darwin-arm64.tar.gz"
      sha256 "4250ce8a74dc154edfc956e724fdae24db14e0b46ca33a9e2274fe3ca26d5639"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.1.1/gmail-to-duckdb-linux-amd64.tar.gz"
      sha256 "a6dd89d62ba2c448145e19f805f4f7a5d07d28e89d1d9412560bdf16c64cdec6"
    end
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.1.1/gmail-to-duckdb-linux-arm64.tar.gz"
      sha256 "d691ab54e07e63c52e3b26937c250ff73578a5e547e8420c4325a8df453c4dbd"
    end
  end

  def install
    bin.install "gmail-to-duckdb"
  end

  test do
    assert_match "serve", shell_output("#{bin}/gmail-to-duckdb help")
  end
end
