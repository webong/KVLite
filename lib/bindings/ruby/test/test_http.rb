require "minitest/autorun"
require "kvlite"

class KVLiteHttpTest < Minitest::Test
  def test_json_requests_include_driver_token_and_ttl
    requests = []
    requester = lambda do |method, url, body, headers|
      requests << [method, url, body, headers]
      method == "GET" ? [200, '{"enabled":true}'] : [204, ""]
    end
    db = KVLite::HttpDatabase.new("http://127.0.0.1:8089", token: "secret",
      driver: "leveldb", requester: requester)
    db.put("flags:101", { enabled: true }, ttl_seconds: 30)
    assert_equal({ "enabled" => true }, db.get("flags:101"))
    db.delete("flags:101")

    assert_equal 3, requests.length
    assert_includes requests[0][1], "ttl_seconds=30"
    assert_equal "Bearer secret", requests[0][3]["Authorization"]
    assert_equal "leveldb", requests[0][3]["X-KVLite-Driver"]
    assert_equal '{"enabled":true}', requests[0][2]
  end

  def test_missing_key_and_invalid_options
    db = KVLite::HttpDatabase.new("https://example.com", requester: ->(*_) { [404, ""] })
    assert_raises(KVLite::NotFoundError) { db.get("missing") }
    assert_raises(KVLite::InvalidArgumentError) { db.put("k", 1, ttl_seconds: -1) }
    assert_raises(KVLite::InvalidArgumentError) { db.get("") }
    assert_raises(KVLite::InvalidArgumentError) { KVLite.connect("file:///tmp/data") }
    assert_raises(KVLite::InvalidArgumentError) { KVLite.connect("http://example.com", driver: "../db") }
  end
end
