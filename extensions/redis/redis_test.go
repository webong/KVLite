package kvliteredis

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webong/kvlite"
	kvlitehttp "github.com/webong/kvlite/extensions/http"
)

type redisTestEngine struct {
	mu          sync.RWMutex
	values      map[string][]byte
	applyError  error
	applyCount  int
	failOnApply int
}

func newRedisTestEngine() *redisTestEngine {
	return &redisTestEngine{values: make(map[string][]byte)}
}

func (engine *redisTestEngine) Get(ctx context.Context, key []byte) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	value, found := engine.values[string(key)]
	return append([]byte(nil), value...), found, nil
}

func (engine *redisTestEngine) Put(ctx context.Context, key, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.values[string(key)] = append([]byte(nil), value...)
	return nil
}

func (engine *redisTestEngine) Delete(ctx context.Context, key []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	delete(engine.values, string(key))
	return nil
}

func (engine *redisTestEngine) Apply(ctx context.Context, mutations []kvlite.Mutation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, mutation := range mutations {
		if len(mutation.Key) == 0 {
			return kvlite.ErrInvalidArgument
		}
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.applyLocked(mutations)
}

func (engine *redisTestEngine) applyLocked(mutations []kvlite.Mutation) error {
	engine.applyCount++
	if engine.applyError != nil || (engine.failOnApply > 0 && engine.applyCount == engine.failOnApply) {
		if engine.applyError == nil {
			return errors.New("injected batch failure")
		}
		return engine.applyError
	}
	for _, mutation := range mutations {
		if mutation.Delete {
			delete(engine.values, string(mutation.Key))
		} else {
			engine.values[string(mutation.Key)] = append([]byte(nil), mutation.Value...)
		}
	}
	return nil
}

func (engine *redisTestEngine) CompareAndApply(ctx context.Context, batch kvlite.ConditionalBatch) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	for _, read := range batch.Reads {
		value, found := engine.values[string(read.Key)]
		if found != read.Found || (found && !bytes.Equal(value, read.Value)) {
			return false, nil
		}
	}
	for _, prefix := range batch.Prefixes {
		expected := make(map[string][]byte, len(prefix.Records))
		for _, record := range prefix.Records {
			expected[string(record.Key)] = record.Value
		}
		matched := 0
		for key, value := range engine.values {
			if !bytes.HasPrefix([]byte(key), prefix.Prefix) {
				continue
			}
			want, found := expected[key]
			if !found || !bytes.Equal(value, want) {
				return false, nil
			}
			matched++
		}
		if matched != len(expected) {
			return false, nil
		}
	}
	if len(batch.Mutations) == 0 {
		return true, nil
	}
	for _, mutation := range batch.Mutations {
		if len(mutation.Key) == 0 {
			return false, kvlite.ErrInvalidArgument
		}
	}
	if err := engine.applyLocked(batch.Mutations); err != nil {
		return false, err
	}
	return true, nil
}

func TestRedisMultiKeyWritesUseOneBatch(t *testing.T) {
	for _, test := range []struct {
		name    string
		command []string
	}{
		{"MSET", []string{"MSET", "one", "new-one", "two", "new-two"}},
		{"MSETNX", []string{"MSETNX", "three", "new-three", "four", "new-four"}},
		{"DEL", []string{"DEL", "one", "two"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage := newRedisTestEngine()
			db, err := kvlite.OpenWithEngine(storage, kvlite.BackendRocksDB)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			server, err := Serve(db, Options{ListenAddress: "127.0.0.1:0"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			client := newRedisTestClient(t, server.URL()[len("redis://"):])
			assertRedisSimple(t, client.do(t, "SET", "one", "old-one"), "OK")
			assertRedisSimple(t, client.do(t, "SET", "two", "old-two"), "OK")
			storage.mu.Lock()
			storage.applyCount = 0
			storage.failOnApply = 2
			storage.mu.Unlock()
			reply := client.do(t, test.command...)
			storage.mu.Lock()
			count := storage.applyCount
			storage.failOnApply = 0
			storage.mu.Unlock()
			if count != 1 {
				t.Fatalf("%s used %d engine batches, want one", test.name, count)
			}
			switch test.name {
			case "MSET":
				assertRedisSimple(t, reply, "OK")
				assertRedisBulk(t, client.do(t, "GET", "one"), "new-one")
				assertRedisBulk(t, client.do(t, "GET", "two"), "new-two")
			case "MSETNX":
				assertRedisInteger(t, reply, 1)
				assertRedisBulk(t, client.do(t, "GET", "three"), "new-three")
				assertRedisBulk(t, client.do(t, "GET", "four"), "new-four")
			case "DEL":
				assertRedisInteger(t, reply, 2)
				assertRedisInteger(t, client.do(t, "EXISTS", "one", "two"), 0)
			}
		})
	}
}

func TestRedisMultiKeyBatchFailureLeavesOldValues(t *testing.T) {
	storage := newRedisTestEngine()
	db, err := kvlite.OpenWithEngine(storage, kvlite.BackendRocksDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server, err := Serve(db, Options{ListenAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	client := newRedisTestClient(t, server.URL()[len("redis://"):])
	assertRedisSimple(t, client.do(t, "SET", "one", "old-one"), "OK")
	assertRedisSimple(t, client.do(t, "SET", "two", "old-two"), "OK")
	storage.mu.Lock()
	storage.applyError = errors.New("injected batch failure")
	storage.mu.Unlock()
	for _, command := range [][]string{
		{"MSET", "one", "new-one", "two", "new-two"},
		{"MSETNX", "three", "new-three", "four", "new-four"},
		{"DEL", "one", "two"},
	} {
		if reply := client.do(t, command...); reply.kind != respError {
			t.Fatalf("%s = %#v, want error", command[0], reply)
		}
	}
	storage.mu.Lock()
	storage.applyError = nil
	storage.mu.Unlock()
	assertRedisBulk(t, client.do(t, "GET", "one"), "old-one")
	assertRedisBulk(t, client.do(t, "GET", "two"), "old-two")
	assertRedisInteger(t, client.do(t, "EXISTS", "three", "four"), 0)
}

func TestAttachedRedisMultiKeyWritesUseOwnerBatch(t *testing.T) {
	storage := newRedisTestEngine()
	owner, err := kvlite.OpenWithEngine(storage, kvlite.BackendRocksDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	httpServer, err := kvlitehttp.Serve(owner, kvlitehttp.Options{ListenAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = httpServer.Close() })
	remote, err := kvlitehttp.Connect(httpServer.URL(), kvlitehttp.ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	redisServer, err := ServeRemote(remote, Options{ListenAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redisServer.Close() })
	client := newRedisTestClient(t, redisServer.URL()[len("redis://"):])
	assertRedisSimple(t, client.do(t, "SET", "one", "old-one"), "OK")
	assertRedisSimple(t, client.do(t, "SET", "two", "old-two"), "OK")
	storage.mu.Lock()
	storage.applyCount = 0
	storage.failOnApply = 2
	storage.mu.Unlock()
	assertRedisSimple(t, client.do(t, "MSET", "one", "new-one", "two", "new-two"), "OK")
	storage.mu.Lock()
	count := storage.applyCount
	storage.failOnApply = 0
	storage.mu.Unlock()
	if count != 1 {
		t.Fatalf("attached MSET used %d owner batches, want one", count)
	}
	assertRedisBulk(t, client.do(t, "GET", "one"), "new-one")
	assertRedisBulk(t, client.do(t, "GET", "two"), "new-two")
	binaryKey := string([]byte{0xff, 0x00, 'x'})
	assertRedisSimple(t, client.do(t, "MSET", binaryKey, "binary", "ascii", "plain"), "OK")
	assertRedisBulk(t, client.do(t, "GET", binaryKey), "binary")
	assertRedisInteger(t, client.do(t, "DEL", binaryKey, "ascii"), 2)
}

func openAttachedRedisPair(t *testing.T) (*redisTestEngine, *redisTestClient, *redisTestClient) {
	t.Helper()
	storage := newRedisTestEngine()
	owner, err := kvlite.OpenWithEngine(storage, kvlite.BackendRocksDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	httpServer, err := kvlitehttp.Serve(owner, kvlitehttp.Options{ListenAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = httpServer.Close() })
	clients := make([]*redisTestClient, 0, 2)
	for range 2 {
		remote, err := kvlitehttp.Connect(httpServer.URL(), kvlitehttp.ClientOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = remote.Close() })
		redisServer, err := ServeRemote(remote, Options{ListenAddress: "127.0.0.1:0"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = redisServer.Close() })
		clients = append(clients, newRedisTestClient(t, redisServer.URL()[len("redis://"):]))
	}
	return storage, clients[0], clients[1]
}

func TestAttachedRedisReadModifyWriteCommandsAreAtomicAcrossClients(t *testing.T) {
	_, first, second := openAttachedRedisPair(t)
	assertRedisSimple(t, first.do(t, "SET", "counter", "0"), "OK")
	assertRedisInteger(t, first.do(t, "HSET", "hash", "counter", "0"), 1)
	assertRedisInteger(t, first.do(t, "LPUSH", "list", "initial"), 1)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, client := range []*redisTestClient{first, second} {
		wg.Add(1)
		go func(client *redisTestClient) {
			defer wg.Done()
			for range 20 {
				if reply := client.do(t, "INCR", "counter"); reply.kind != respInteger {
					results <- fmt.Errorf("INCR = %#v", reply)
					return
				}
				if reply := client.do(t, "HINCRBY", "hash", "counter", "1"); reply.kind != respInteger {
					results <- fmt.Errorf("HINCRBY = %#v", reply)
					return
				}
				if reply := client.do(t, "LPUSH", "list", "item"); reply.kind != respInteger {
					results <- fmt.Errorf("LPUSH = %#v", reply)
					return
				}
			}
			results <- nil
		}(client)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertRedisBulk(t, first.do(t, "GET", "counter"), "40")
	assertRedisBulk(t, second.do(t, "HGET", "hash", "counter"), "40")
	assertRedisInteger(t, first.do(t, "LLEN", "list"), 41)
}

func TestAttachedRedisMGetSeesOneOwnerSnapshot(t *testing.T) {
	_, writer, reader := openAttachedRedisPair(t)
	assertRedisSimple(t, writer.do(t, "MSET", "pair:a", "0", "pair:b", "0"), "OK")
	finished := make(chan error, 1)
	go func() {
		for index := 1; index <= 40; index++ {
			value := fmt.Sprint(index)
			if reply := writer.do(t, "MSET", "pair:a", value, "pair:b", value); reply.kind != respSimple {
				finished <- fmt.Errorf("MSET = %#v", reply)
				return
			}
		}
		finished <- nil
	}()
	for range 40 {
		reply := reader.do(t, "MGET", "pair:a", "pair:b")
		if reply.kind != respArray || len(reply.items) != 2 || reply.items[0].kind != respBulk || reply.items[1].kind != respBulk || !bytes.Equal(reply.items[0].data, reply.items[1].data) {
			t.Fatalf("MGET observed a mixed snapshot: %#v", reply)
		}
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestAttachedRedisFailedReadModifyWritePreservesOldData(t *testing.T) {
	storage, first, second := openAttachedRedisPair(t)
	assertRedisSimple(t, first.do(t, "SET", "key", "old"), "OK")
	assertRedisInteger(t, first.do(t, "HSET", "hash", "field", "7"), 1)
	assertRedisInteger(t, first.do(t, "RPUSH", "list", "first", "second"), 2)
	storage.mu.Lock()
	storage.applyError = errors.New("injected batch failure")
	storage.mu.Unlock()
	for _, command := range [][]string{
		{"GETSET", "key", "new"},
		{"HINCRBY", "hash", "field", "1"},
		{"LPOP", "list"},
	} {
		if reply := second.do(t, command...); reply.kind != respError {
			t.Fatalf("%s = %#v, want an error", command[0], reply)
		}
	}
	assertRedisBulk(t, first.do(t, "GET", "key"), "old")
	storage.mu.Lock()
	storage.applyError = nil
	storage.mu.Unlock()
	assertRedisBulk(t, first.do(t, "GET", "key"), "old")
	assertRedisBulk(t, first.do(t, "HGET", "hash", "field"), "7")
	list := first.do(t, "LRANGE", "list", "0", "-1")
	if list.kind != respArray || len(list.items) != 2 || string(list.items[0].data) != "first" || string(list.items[1].data) != "second" {
		t.Fatalf("list after failed pop = %#v", list)
	}
}

func TestAttachedRedisUsesConditionalBatchOnEmbeddedOwner(t *testing.T) {
	owner, err := kvlite.Open(t.TempDir(), kvlite.WithDriver("memory"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	httpServer, err := kvlitehttp.Serve(owner, kvlitehttp.Options{ListenAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = httpServer.Close() })
	remote, err := kvlitehttp.Connect(httpServer.URL(), kvlitehttp.ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	redisServer, err := ServeRemote(remote, Options{ListenAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redisServer.Close() })
	client := newRedisTestClient(t, redisServer.URL()[len("redis://"):])
	assertRedisSimple(t, client.do(t, "SET", "value", "7"), "OK")
	assertRedisBulk(t, client.do(t, "GETSET", "value", "8"), "7")
	assertRedisInteger(t, client.do(t, "INCR", "value"), 9)
	assertRedisInteger(t, client.do(t, "HSET", "hash", "field", "4"), 1)
	assertRedisInteger(t, client.do(t, "HINCRBY", "hash", "field", "3"), 7)
	assertRedisInteger(t, client.do(t, "SADD", "set", "one", "two"), 2)
	assertRedisInteger(t, client.do(t, "SCARD", "set"), 2)
	assertRedisInteger(t, client.do(t, "LPUSH", "list", "one", "two"), 2)
	assertRedisBulk(t, client.do(t, "RPOP", "list"), "one")
	assertRedisInteger(t, client.do(t, "EXPIRE", "value", "60"), 1)
	assertRedisInteger(t, client.do(t, "PERSIST", "value"), 1)
	if reply := client.do(t, "MGET", "value", "missing"); reply.kind != respArray || len(reply.items) != 2 || string(reply.items[0].data) != "9" || !reply.items[1].null {
		t.Fatalf("MGET = %#v", reply)
	}
	if reply := client.do(t, "KEYS", "*"); reply.kind != respArray || len(reply.items) != 4 {
		t.Fatalf("KEYS = %#v", reply)
	}
	secondRemote, err := kvlitehttp.Connect(httpServer.URL(), kvlitehttp.ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondRemote.Close() })
	secondServer, err := ServeRemote(secondRemote, Options{ListenAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondServer.Close() })
	secondClient := newRedisTestClient(t, secondServer.URL()[len("redis://"):])
	var wg sync.WaitGroup
	for _, attached := range []*redisTestClient{client, secondClient} {
		wg.Add(1)
		go func(attached *redisTestClient) {
			defer wg.Done()
			for range 10 {
				if reply := attached.do(t, "INCR", "value"); reply.kind != respInteger {
					t.Errorf("embedded owner INCR = %#v", reply)
					return
				}
			}
		}(attached)
	}
	wg.Wait()
	assertRedisBulk(t, client.do(t, "GET", "value"), "29")
	assertRedisSimple(t, client.do(t, "FLUSHDB"), "OK")
	assertRedisInteger(t, client.do(t, "DBSIZE"), 0)
}

func TestFailedRedisSetPreservesOldCollection(t *testing.T) {
	storage := newRedisTestEngine()
	db, err := kvlite.OpenWithEngine(storage, kvlite.BackendRemote)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.SAdd(ctx, "shared", "first", "second"); err != nil {
		t.Fatal(err)
	}
	storage.mu.Lock()
	storage.applyError = errors.New("injected batch failure")
	storage.mu.Unlock()
	redisDB := newDatabase(db.Protocol())
	reply := redisDB.redisSet([][]byte{[]byte("SET"), []byte("shared"), []byte("scalar")})
	if reply.kind != respError {
		t.Fatalf("SET reply = %#v, want error", reply)
	}
	members, err := db.SMembers(ctx, "shared")
	if err != nil || !slices.Equal(members, []string{"first", "second"}) {
		t.Fatalf("old set changed after failed SET: %v, %v", members, err)
	}
}

func (engine *redisTestEngine) ScanPrefix(ctx context.Context, prefix []byte, callback func(key, value []byte) error) error {
	engine.mu.RLock()
	keys := make([]string, 0, len(engine.values))
	for key := range engine.values {
		if bytes.HasPrefix([]byte(key), prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	items := make([][2][]byte, 0, len(keys))
	for _, key := range keys {
		items = append(items, [2][]byte{[]byte(key), append([]byte(nil), engine.values[key]...)})
	}
	engine.mu.RUnlock()
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := callback(item[0], item[1]); err != nil {
			return err
		}
	}
	return nil
}

func (engine *redisTestEngine) Close() error { return nil }

func openRedisTestServer(t *testing.T, options Options) (*kvlite.DB, *Server) {
	t.Helper()
	db, err := kvlite.OpenWithEngine(newRedisTestEngine(), kvlite.BackendRocksDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server, err := Serve(db, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return db, server
}

type redisTestClient struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

func newRedisTestClient(t *testing.T, address string) *redisTestClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client := &redisTestClient{conn: conn, reader: bufio.NewReader(conn), writer: bufio.NewWriter(conn)}
	t.Cleanup(func() { _ = conn.Close() })
	return client
}

func (client *redisTestClient) do(t *testing.T, args ...string) respValue {
	t.Helper()
	items := make([]respValue, 0, len(args))
	for _, arg := range args {
		items = append(items, respBulkString(arg))
	}
	if err := writeRESP(client.writer, respArrayValues(items...)); err != nil {
		t.Fatal(err)
	}
	if err := client.writer.Flush(); err != nil {
		t.Fatal(err)
	}
	reply, err := readRESP(client.reader)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func assertRedisSimple(t *testing.T, reply respValue, want string) {
	t.Helper()
	if reply.kind != respSimple || string(reply.data) != want {
		t.Fatalf("reply = %#v, want +%s", reply, want)
	}
}

func assertRedisBulk(t *testing.T, reply respValue, want string) {
	t.Helper()
	if reply.kind != respBulk || reply.null || !bytes.Equal(reply.data, []byte(want)) {
		t.Fatalf("reply = %#v, want $%q", reply, want)
	}
}

func assertRedisInteger(t *testing.T, reply respValue, want int64) {
	t.Helper()
	if reply.kind != respInteger || reply.value != want {
		t.Fatalf("reply = %#v, want :%d", reply, want)
	}
}

func TestRESPAllowsNullReplyItemsButRejectsNullCommandArguments(t *testing.T) {
	value, err := readRESP(bufio.NewReader(strings.NewReader("*2\r\n$3\r\nGET\r\n$-1\r\n")))
	if err != nil || len(value.items) != 2 || !value.items[1].null {
		t.Fatalf("null array item = %#v, %v", value, err)
	}
	if _, err := redisCommandArgs(value); err == nil {
		t.Fatal("null command argument unexpectedly accepted")
	}
}

func TestRedisRESPCompatibility(t *testing.T) {
	_, server := openRedisTestServer(t, Options{
		ListenAddress: "127.0.0.1:0",
		Password:      "secret",
	})
	client := newRedisTestClient(t, server.URL()[len("redis://"):])
	if reply := client.do(t, "PING"); reply.kind != respError || string(reply.data) != "NOAUTH Authentication required." {
		t.Fatalf("unauthenticated PING = %#v", reply)
	}
	if reply := client.do(t, "AUTH", "wrong"); reply.kind != respError {
		t.Fatalf("wrong AUTH = %#v", reply)
	}
	if reply := client.do(t, "HELLO", "3", "AUTH", "default", "wrong"); reply.kind != respError {
		t.Fatalf("wrong HELLO AUTH = %#v", reply)
	}
	if reply := client.do(t, "HELLO", "3", "AUTH", "default", "secret"); reply.kind != respMap || len(reply.items) == 0 {
		t.Fatalf("HELLO 3 = %#v", reply)
	}
	assertRedisSimple(t, client.do(t, "PING"), "PONG")

	assertRedisSimple(t, client.do(t, "SET", "foo", "bar", "EX", "60"), "OK")
	assertRedisBulk(t, client.do(t, "GET", "foo"), "bar")
	if reply := client.do(t, "TTL", "foo"); reply.kind != respInteger || reply.value < 1 || reply.value > 60 {
		t.Fatalf("TTL foo = %#v", reply)
	}
	assertRedisSimple(t, client.do(t, "SET", "ab", "sibling"), "OK")
	assertRedisInteger(t, client.do(t, "DEL", "foo"), 1)
	assertRedisBulk(t, client.do(t, "GET", "ab"), "sibling")

	assertRedisInteger(t, client.do(t, "HSET", "profile", "name", "Ada", "city", "Lagos"), 2)
	assertRedisBulk(t, client.do(t, "HGET", "profile", "name"), "Ada")
	if reply := client.do(t, "HGETALL", "profile"); reply.kind != respArray || len(reply.items) != 4 {
		t.Fatalf("HGETALL profile = %#v", reply)
	}
	assertRedisInteger(t, client.do(t, "SADD", "roles", "admin", "author", "admin"), 2)
	if reply := client.do(t, "SMEMBERS", "roles"); reply.kind != respArray || len(reply.items) != 2 {
		t.Fatalf("SMEMBERS roles = %#v", reply)
	}
	assertRedisInteger(t, client.do(t, "LPUSH", "jobs", "one", "two"), 2)
	if reply := client.do(t, "LRANGE", "jobs", "0", "-1"); reply.kind != respArray || len(reply.items) != 2 {
		t.Fatalf("LRANGE jobs = %#v", reply)
	} else {
		assertRedisBulk(t, reply.items[0], "two")
		assertRedisBulk(t, reply.items[1], "one")
	}
	if reply := client.do(t, "HGET", "ab", "field"); reply.kind != respError || string(reply.data) != errRedisWrongType.Error() {
		t.Fatalf("wrong type reply = %#v", reply)
	}
	assertRedisInteger(t, client.do(t, "INCRBY", "counter", "41"), 41)
	assertRedisInteger(t, client.do(t, "INCR", "counter"), 42)
	assertRedisBulk(t, client.do(t, "GET", "counter"), "42")
}

func TestRedisTTLAndCollectionsExpire(t *testing.T) {
	db, server := openRedisTestServer(t, Options{ListenAddress: "127.0.0.1:0"})
	client := newRedisTestClient(t, server.URL()[len("redis://"):])
	assertRedisSimple(t, client.do(t, "SET", "ephemeral", "value"), "OK")
	assertRedisInteger(t, client.do(t, "PEXPIRE", "ephemeral", "30"), 1)
	time.Sleep(60 * time.Millisecond)
	if reply := client.do(t, "GET", "ephemeral"); reply.kind != respBulk || !reply.null {
		t.Fatalf("expired GET = %#v", reply)
	}
	assertRedisInteger(t, client.do(t, "TTL", "ephemeral"), -2)
	assertRedisInteger(t, client.do(t, "SADD", "short", "member"), 1)
	assertRedisInteger(t, client.do(t, "PEXPIRE", "short", "30"), 1)
	time.Sleep(60 * time.Millisecond)
	if reply := client.do(t, "SCARD", "short"); reply.kind != respInteger || reply.value != 0 {
		t.Fatalf("expired set SCARD = %#v", reply)
	}
	if exists, err := db.Has(context.Background(), "ephemeral"); err != nil || exists {
		t.Fatalf("Has expired key = %t, %v", exists, err)
	}
}

func TestClosingServerLeavesEmbeddedOwnerOpen(t *testing.T) {
	db, server := openRedisTestServer(t, Options{ListenAddress: "127.0.0.1:0"})
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Put(context.Background(), "embedded", "still-open"); err != nil {
		t.Fatalf("owner Put() after Server.Close() = %v", err)
	}
}

func TestServeRemoteRejectsNilDatabase(t *testing.T) {
	if _, err := ServeRemote(nil, Options{ListenAddress: "127.0.0.1:0"}); err == nil {
		t.Fatal("ServeRemote(nil) unexpectedly succeeded")
	}
}

func TestServeRemoteServesRemoteHandle(t *testing.T) {
	db, err := kvlite.OpenWithEngine(newRedisTestEngine(), kvlite.BackendRemote)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if !db.IsRemote() {
		t.Fatal("OpenWithEngine handle is not marked remote")
	}
	server, err := ServeRemote(db, Options{ListenAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	client := newRedisTestClient(t, server.URL()[len("redis://"):])
	assertRedisSimple(t, client.do(t, "PING"), "PONG")
	assertRedisSimple(t, client.do(t, "SET", "attached", "yes"), "OK")
	assertRedisBulk(t, client.do(t, "GET", "attached"), "yes")
	assertRedisInteger(t, client.do(t, "HSET", "profile", "name", "Ada"), 1)
	assertRedisBulk(t, client.do(t, "HGET", "profile", "name"), "Ada")
}
