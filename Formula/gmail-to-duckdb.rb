class GmailToDuckdb < Formula
  desc "Sync Gmail into a local DuckDB file"
  homepage "https://github.com/ashwath-ramesh/gmail-to-duckdb"
  version "0.3.1"

  on_macos do
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.3.1/gmail-to-duckdb-darwin-arm64.tar.gz"
      sha256 "573a1fbda9d49f3deafca53ca1cee6ce4cd79f2e91da9f534ab86f123ee873fd"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.3.1/gmail-to-duckdb-linux-amd64.tar.gz"
      sha256 "42a0c1ec96656b076c5b83bd0bc66bee4b591826565522772e079dcc2abb907b"
    end
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.3.1/gmail-to-duckdb-linux-arm64.tar.gz"
      sha256 "cc353c21333c4676698a0d130ec33d01d5c6193252951f8d622aaae6c7777bcc"
    end
  end

  def install
    bin.install "gmail-to-duckdb"
  end

  test do
    assert_match "serve", shell_output("#{bin}/gmail-to-duckdb help")
  end
end
