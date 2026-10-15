# typed: false
# frozen_string_literal: true

# KVLite LMDB driver bundle. CGo, but it needs no system LMDB library: the
# pinned PowerDNS binding compiles the bundled LMDB 0.9 C source, so the
# bundle installs its own notices. Ships only its versioned catalog subtree;
# any installed kvlite host drives it, so this package owns no bin/kvlite
# link. See packaging/README.md.
class KvliteLmdb < Formula
  desc "KVLite LMDB storage driver bundle (bundled LMDB C source)"
  homepage "https://github.com/webong/KVlite"
  # Tag and revision are stamped by scripts/stamp-packaging.py at release time.
  url "https://github.com/webong/KVlite.git",
      tag: "v0.1.0"
  license "Apache-2.0"
  version "0.1.0"

  depends_on "go" => :build
  depends_on "kvlite"

  def install
    ENV["CGO_ENABLED"] = "1"
    system "make", "release", "RELEASE_VERSION=#{version}", "DRIVER=lmdb"
    system "bash", "scripts/install.sh",
           "--prefix", "#{prefix}",
           "--version", "#{version}",
           "--no-cli-link",
           "--component", "drivers"
  end

  def caveats
    <<~EOS
      Serve it through any installed kvlite host:

        export KVLITE_SYSTEM_MODULE_PATH="#{opt_lib}/kvlite"
        kvlite serve --path ./data --driver lmdb --listen 127.0.0.1:8080

      The bundle reserves a 16 GiB virtual map by default and includes the
      LMDB (OpenLDAP) and lmdb-go (BSD-3-Clause) notices under its NOTICES/.
    EOS
  end

  test do
    ENV["KVLITE_SYSTEM_MODULE_PATH"] = "#{opt_lib}/kvlite"
    ENV["KVLITE_MODULE_PATH"] = ""
    ENV["KVLITE_HOME"] = ""
    host = Formula["kvlite"].opt_bin/"kvlite"
    system host.to_s, "module", "verify", "lmdb"
  end
end
