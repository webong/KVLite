require_relative "lib/kvlite/version"

Gem::Specification.new do |spec|
  spec.name = "kvlite"
  spec.version = KVLite::VERSION
  spec.summary = "Embedded and HTTP Ruby client for KVLite"
  spec.description = "Thin Ruby binding for KVLite's versioned native C ABI and optional HTTP transport"
  spec.authors = ["Webong"]
  spec.homepage = "https://github.com/webong/KVlite"
  spec.license = "MIT"
  spec.required_ruby_version = ">= 3.1"
  spec.files = Dir["lib/**/*.rb"] + %w[LICENSE README.md]
  spec.require_paths = ["lib"]
  spec.metadata = {
    "homepage_uri" => spec.homepage,
    "source_uri" => "https://github.com/webong/KVlite/tree/main/lib/bindings/ruby",
    "issues_uri" => "https://github.com/webong/KVlite/issues"
  }
end
