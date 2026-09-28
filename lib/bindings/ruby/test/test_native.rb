require "minitest/autorun"
require "tmpdir"
require "fileutils"
require "kvlite"

class KVLiteNativeTest < Minitest::Test
  def test_json_and_binary_round_trip
    with_mock_library do |library|
      db = KVLite.open("mock-db", library_path: library, driver: "leveldb")
      db.put("user:101", { name: "Ada" }, ttl_seconds: 60)
      assert_equal({ "name" => "Ada" }, db.get("user:101"))

      db.put_bytes("binary\0key", "value\0bytes")
      assert_equal "value\0bytes".b, db.get_bytes("binary\0key")
      db.delete_bytes("binary\0key")
      assert_raises(KVLite::NotFoundError) { db.get_bytes("binary\0key") }
      db.close
      db.close
      assert_raises(KVLite::StorageError) { db.get("user:101") }
    end
  end

  def test_legacy_library_allows_default_open_but_not_driver_selection
    with_mock_library("-DKVLITE_MOCK_LEGACY_ABI") do |library|
      assert_raises(KVLite::NativeLibraryError) do
        KVLite.open("mock-db", library_path: library, driver: "leveldb")
      end
      db = KVLite.open("mock-db", library_path: library)
      db.close
    end
  end

  def test_catalog_discovery_and_input_validation
    with_mock_library do |library|
      Dir.mktmpdir("kvlite-ruby-catalog-") do |root|
        target = File.join(root, "drivers", "leveldb", "lib", KVLite::LibraryFinder.library_name)
        FileUtils.mkdir_p(File.dirname(target))
        FileUtils.cp(library, target)
        old_home, old_override = ENV.values_at("KVLITE_HOME", "KVLITE_LIBRARY_PATH")
        begin
          ENV["KVLITE_HOME"] = root
          ENV.delete("KVLITE_LIBRARY_PATH")
          assert_equal File.expand_path(target), KVLite::LibraryFinder.find(driver: "leveldb")
          db = KVLite.open("mock-db", driver: "leveldb")
          assert_raises(KVLite::InvalidArgumentError) { db.put_bytes("", "value") }
          assert_raises(KVLite::InvalidArgumentError) { db.put("key", 1, ttl_seconds: -1) }
          db.close
        ensure
          ENV["KVLITE_HOME"] = old_home
          ENV["KVLITE_LIBRARY_PATH"] = old_override
        end
      end
    end
    assert_raises(KVLite::InvalidArgumentError) { KVLite.open("mock-db", driver: "../leveldb") }
    assert_raises(KVLite::NativeLibraryError) { KVLite.open("mock-db", library_path: "missing") }
  end

  def test_real_native_bundle_when_available
    library = ENV["KVLITE_TEST_NATIVE_LIBRARY"]
    skip "set KVLITE_TEST_NATIVE_LIBRARY for bundle integration" if library.nil? || library.empty?

    Dir.mktmpdir("kvlite-ruby-real-") do |root|
      db = KVLite.open(File.join(root, "db"), library_path: library, driver: "leveldb")
      db.put("integration", { language: "ruby" })
      assert_equal({ "language" => "ruby" }, db.get("integration"))
      db.close
    end
  end

  private

  def with_mock_library(*flags)
    Dir.mktmpdir("kvlite-ruby-native-") do |root|
      fixture = File.join(__dir__, "mock_kvlite.c")
      fixture = File.expand_path("../../test-fixtures/mock_kvlite.c", __dir__) unless File.file?(fixture)
      library = File.join(root, KVLite::LibraryFinder.library_name)
      platform_flag = RUBY_PLATFORM.include?("darwin") ? "-dynamiclib" : "-shared"
      assert system("cc", platform_flag, "-fPIC", *flags, fixture, "-o", library), "mock C library did not compile"
      yield library
    end
  end
end
