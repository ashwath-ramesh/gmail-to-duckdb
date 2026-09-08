class GmailToDuckdb < Formula
  desc "Sync Gmail into a local DuckDB file"
  homepage "https://github.com/ashwath-ramesh/gmail-to-duckdb"
  version "0.1.0"

  on_macos do
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.1.0/gmail-to-duckdb-darwin-arm64.tar.gz"
      sha256 "f37c34e154682cf1282e5a34803eb9692c69999178c1be109d15459353c8ea79"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.1.0/gmail-to-duckdb-linux-amd64.tar.gz"
      sha256 "e7231fd7fc30b5648b90820ffbe9adc1762b03d145e2a40fabcd33cd9c7a5520"
    end
    on_arm do
      url "https://github.com/ashwath-ramesh/gmail-to-duckdb/releases/download/v0.1.0/gmail-to-duckdb-linux-arm64.tar.gz"
      sha256 "ae8c72453f62e10310ea9abdf6e9a64782208eacb819b26da23cc29b91fbceae"
    end
  end

  def install
    bin.install "gmail-to-duckdb"
  end

  test do
    assert_match "serve", shell_output("#{bin}/gmail-to-duckdb help")
  end
end
