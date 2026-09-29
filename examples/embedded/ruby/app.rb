require "kvlite"

path = ENV.fetch("KVLITE_DB_PATH", File.join(__dir__, "data"))
driver = ENV.fetch("KVLITE_DRIVER", "leveldb")
db = KVLite.open(path, driver: driver)

begin
  db.put("user:101", { id: 101, name: "Ada" }, ttl_seconds: 3600)
  puts "user: #{db.get('user:101')}"

  db.put_bytes("blob:101", "\x00\x01\x02".b)
  puts "bytes: #{db.get_bytes('blob:101').unpack1('H*')}"
ensure
  db.close
end
