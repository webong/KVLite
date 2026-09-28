require "rbconfig"

module KVLite
  class LibraryFinder
    def self.find(path: nil, driver: nil)
      return require_file(path) unless path.nil?
      override = ENV["KVLITE_LIBRARY_PATH"]
      return require_file(override) unless override.nil? || override.empty?

      roots = [ENV["KVLITE_HOME"]]
      roots.concat((ENV["KVLITE_SYSTEM_MODULE_PATH"] || "").split(File::PATH_SEPARATOR))
      roots << File.join(Dir.home, ".local", "lib", "kvlite")
      roots.concat(["/usr/local/lib/kvlite", "/usr/lib/kvlite"])

      roots.compact.reject(&:empty?).uniq.each do |root|
        candidates = []
        prefer_host = !(RbConfig::CONFIG["host_os"] =~ /darwin/ && RbConfig::CONFIG["host_cpu"] =~ /x86_64|amd64/)
        candidates.concat([File.join(root, "host", "lib", library_name), File.join(root, "lib", library_name)]) if prefer_host
        candidates << File.join(root, "drivers", driver, "lib", library_name) if driver
        sole = sole_driver_bundle(root)
        candidates << sole if sole
        candidates.concat([File.join(root, "host", "lib", library_name), File.join(root, "lib", library_name)]) unless prefer_host
        found = candidates.find { |candidate| File.file?(candidate) }
        return File.expand_path(found) if found
      end

      raise NativeLibraryError,
        "KVLite native library was not found. Set KVLITE_LIBRARY_PATH or install a matching driver bundle."
    end

    def self.library_name
      platform = RbConfig::CONFIG["host_os"]
      return "kvlite.dll" if platform =~ /mswin|mingw|cygwin/
      return "libkvlite.dylib" if platform =~ /darwin/

      "libkvlite.so"
    end

    def self.require_file(path)
      raise NativeLibraryError, "KVLite native library path is required" if path.to_s.empty?
      raise NativeLibraryError, "KVLite native library does not exist: #{path}" unless File.file?(path)

      File.expand_path(path)
    end

    def self.sole_driver_bundle(root)
      paths = Dir.children(File.join(root, "drivers")).map do |entry|
        File.join(root, "drivers", entry, "lib", library_name)
      end.select { |candidate| File.file?(candidate) }
      paths.one? ? paths.first : nil
    rescue Errno::ENOENT, Errno::EACCES
      nil
    end
  end
end
