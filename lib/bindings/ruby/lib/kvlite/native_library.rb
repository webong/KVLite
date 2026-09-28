require "fiddle"

module KVLite
  # The only embedded seam: the stable, allocator-owned KVLite C ABI.
  class NativeLibrary
    ABI_VERSION = 1
    POINTER_SIZE = Fiddle::SIZEOF_VOIDP
    HANDLE_SIZE = Fiddle::SIZEOF_LONG_LONG
    MAX_TTL_SECONDS = (2**63 - 1) / 1_000_000_000

    def initialize(path)
      # Keep ABI symbols local so another KVLite library cannot intercept calls.
      @handle = Fiddle::Handle.new(path, Fiddle::RTLD_NOW)
      # libkvlite is a Go c-shared library. Its runtime cannot be safely
      # unloaded while this process is alive, even after a DB handle closes.
      @handle.disable_close
      abi = bind("kvlite_abi_version", [], Fiddle::TYPE_INT)
      raise NativeLibraryError, "KVLite native ABI mismatch: Ruby requires ABI 1" unless abi.call == ABI_VERSION

      @open = bind("kvlite_open", [Fiddle::TYPE_VOIDP, Fiddle::TYPE_VOIDP, Fiddle::TYPE_VOIDP])
      @open_driver = bind_optional("kvlite_open_with_driver",
        [Fiddle::TYPE_VOIDP, Fiddle::TYPE_VOIDP, Fiddle::TYPE_VOIDP, Fiddle::TYPE_VOIDP]) ||
        bind_optional("kvlite_open_with_backend",
          [Fiddle::TYPE_VOIDP, Fiddle::TYPE_VOIDP, Fiddle::TYPE_VOIDP, Fiddle::TYPE_VOIDP])
      @close = bind("kvlite_close", [Fiddle::TYPE_LONG_LONG, Fiddle::TYPE_VOIDP])
      @put = bind("kvlite_put", [Fiddle::TYPE_LONG_LONG, Fiddle::TYPE_VOIDP, Fiddle::TYPE_SIZE_T,
        Fiddle::TYPE_VOIDP, Fiddle::TYPE_SIZE_T, Fiddle::TYPE_LONG_LONG, Fiddle::TYPE_VOIDP])
      @get = bind("kvlite_get", [Fiddle::TYPE_LONG_LONG, Fiddle::TYPE_VOIDP, Fiddle::TYPE_SIZE_T,
        Fiddle::TYPE_VOIDP, Fiddle::TYPE_VOIDP, Fiddle::TYPE_VOIDP])
      @delete = bind("kvlite_delete", [Fiddle::TYPE_LONG_LONG, Fiddle::TYPE_VOIDP,
        Fiddle::TYPE_SIZE_T, Fiddle::TYPE_VOIDP])
      @free = bind("kvlite_free", [Fiddle::TYPE_VOIDP], Fiddle::TYPE_VOID)
    rescue Fiddle::DLError => error
      raise NativeLibraryError, "Unable to load KVLite native library at #{path}: #{error.message}"
    end

    def open(path, driver)
      if driver && !@open_driver
        raise NativeLibraryError, "This libkvlite does not support selecting a storage driver"
      end
      with_slots(HANDLE_SIZE, POINTER_SIZE) do |out_handle, out_error|
        status = if driver
          @open_driver.call(path, driver, out_handle, out_error)
        else
          @open.call(path, out_handle, out_error)
        end
        check_status(status, out_error)
        out_handle[0, HANDLE_SIZE].unpack1("Q")
      end
    end

    def close(handle)
      with_slots(POINTER_SIZE) do |out_error|
        check_status(@close.call(handle, out_error), out_error)
      end
    end

    def put(handle, key, value, ttl_seconds)
      with_slots(POINTER_SIZE) do |out_error|
        status = @put.call(handle, key, key.bytesize, value, value.bytesize, ttl_seconds, out_error)
        check_status(status, out_error)
      end
    end

    def get(handle, key)
      with_slots(POINTER_SIZE, POINTER_SIZE, POINTER_SIZE) do |out_value, out_length, out_error|
        status = @get.call(handle, key, key.bytesize, out_value, out_length, out_error)
        address = out_value[0, POINTER_SIZE].unpack1("J")
        begin
          check_status(status, out_error)
          length = out_length[0, POINTER_SIZE].unpack1("J")
          raise StorageError, "KVLite native library returned an invalid value pointer" if address.zero? && length.positive?
          return "".b if length.zero?

          Fiddle::Pointer.new(address).to_s(length).b
        ensure
          @free.call(address) unless address.zero?
        end
      end
    end

    def delete(handle, key)
      with_slots(POINTER_SIZE) do |out_error|
        check_status(@delete.call(handle, key, key.bytesize, out_error), out_error)
      end
    end

    private

    def bind(name, arguments, result = Fiddle::TYPE_INT)
      Fiddle::Function.new(@handle[name], arguments, result)
    end

    def bind_optional(name, arguments)
      bind(name, arguments)
    rescue Fiddle::DLError
      nil
    end

    def with_slots(*sizes, &block)
      slots = []
      sizes.each do |size|
        slot = Fiddle::Pointer.malloc(size)
        slot[0, size] = "\0" * size
        slots << slot
      end
      block.call(*slots)
    ensure
      slots.each { |slot| Fiddle.free(slot.to_i) }
    end

    def check_status(status, out_error)
      address = out_error[0, POINTER_SIZE].unpack1("J")
      message = if address.zero?
        "KVLite native operation failed"
      else
        begin
          Fiddle::Pointer.new(address).to_s
        ensure
          @free.call(address)
        end
      end
      case status
      when 0 then nil
      when 1 then raise NotFoundError, message
      when 2 then raise InvalidArgumentError, message
      else raise StorageError, message
      end
    end
  end
end
