module KVLite
  class Error < StandardError; end
  class NotFoundError < Error; end
  class InvalidArgumentError < Error; end
  class StorageError < Error; end
  class SerializationError < Error; end
  class NativeLibraryError < Error; end
end
