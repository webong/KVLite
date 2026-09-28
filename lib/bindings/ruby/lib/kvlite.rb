require_relative "kvlite/version"
require_relative "kvlite/errors"
require_relative "kvlite/native_database"
require_relative "kvlite/http_database"

module KVLite
  def self.open(path, library_path: nil, driver: nil)
    NativeDatabase.open(path, library_path: library_path, driver: driver)
  end

  def self.connect(base_url, token: nil, timeout_seconds: 30, driver: nil)
    HttpDatabase.new(base_url, token: token, timeout_seconds: timeout_seconds, driver: driver)
  end
end
