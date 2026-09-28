require "json"
require "thread"
require_relative "errors"
require_relative "library_finder"
require_relative "native_library"

module KVLite
  class NativeDatabase
    def self.open(path, library_path: nil, driver: nil)
      unless path.is_a?(String) && !path.empty? && !path.include?("\0")
        raise InvalidArgumentError, "KVLite database path is required and cannot contain NUL"
      end
      driver = normalize_driver(driver)
      library = NativeLibrary.new(LibraryFinder.find(path: library_path, driver: driver || "rocksdb"))
      new(library, library.open(path, driver))
    end

    def self.normalize_driver(driver)
      return nil if driver.nil?
      unless driver.is_a?(String) && driver.strip.downcase.match?(/\A[a-z0-9][a-z0-9_-]*\z/)
        raise InvalidArgumentError, "KVLite driver must be a name, not a filesystem path"
      end

      driver.strip.downcase
    end

    def initialize(library, handle)
      @library = library
      @handle = handle
      @mutex = Mutex.new
    end

    def put(key, value, ttl_seconds: 0)
      payload = JSON.generate(value)
      put_bytes(key, payload, ttl_seconds: ttl_seconds)
    rescue JSON::GeneratorError => error
      raise SerializationError, "KVLite JSON serialization failed: #{error.message}"
    end

    def get(key)
      JSON.parse(get_bytes(key))
    rescue JSON::ParserError => error
      raise SerializationError, "KVLite JSON deserialization failed: #{error.message}"
    end

    def put_bytes(key, value, ttl_seconds: 0)
      key = binary_key(key)
      raise InvalidArgumentError, "KVLite value must be a String" unless value.is_a?(String)
      unless ttl_seconds.is_a?(Integer) && ttl_seconds.between?(0, NativeLibrary::MAX_TTL_SECONDS)
        raise InvalidArgumentError, "KVLite TTL must be a non-negative number of seconds"
      end

      @mutex.synchronize do
        ensure_open
        @library.put(@handle, key, value.b, ttl_seconds)
      end
      nil
    end

    def get_bytes(key)
      key = binary_key(key)
      @mutex.synchronize do
        ensure_open
        @library.get(@handle, key)
      end
    end

    def delete(key)
      delete_bytes(key)
    end

    def delete_bytes(key)
      key = binary_key(key)
      @mutex.synchronize do
        ensure_open
        @library.delete(@handle, key)
      end
      nil
    end

    def close
      @mutex.synchronize do
        return if @handle.nil?

        @library.close(@handle)
        @handle = nil
      end
      nil
    end

    private

    def binary_key(key)
      raise InvalidArgumentError, "KVLite key must be a non-empty String" unless key.is_a?(String) && !key.empty?

      key.b
    end

    def ensure_open
      raise StorageError, "KVLite database is closed" if @handle.nil?
    end
  end
end
