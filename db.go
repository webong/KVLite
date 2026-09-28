// Package kvlite is a thin Go binding for KVLite's installed C ABI.
// It does not import or compile the KVLite storage implementation.
package kvlite

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	ErrNotFound        = errors.New("kvlite: key not found")
	ErrClosed          = errors.New("kvlite: database is closed")
	ErrInvalidArgument = errors.New("kvlite: invalid argument")
	ErrNativeLibrary   = errors.New("kvlite: native library unavailable")
	ErrStorage         = errors.New("kvlite: storage error")
)

// DB owns one embedded database handle. A database directory has one owner
// process; use an optional KVLite transport for cross-process access.
type DB struct{ native nativeDB }

type nativeDB interface {
	put(key, value []byte, ttlSeconds int64) error
	get(key []byte) ([]byte, error)
	delete(key []byte) error
	close() error
}

type config struct {
	driver         string
	libraryPath    string
	explicitDriver bool
}

// Option changes how a native KVLite bundle is selected.
type Option func(*config) error

// WithDriver selects an installed storage engine. A database directory records
// its driver and cannot be reopened with another driver.
func WithDriver(name string) Option {
	return func(c *config) error {
		name = strings.ToLower(strings.TrimSpace(name))
		if !validDriver(name) {
			return fmt.Errorf("%w: invalid driver name %q", ErrInvalidArgument, name)
		}
		c.driver = name
		c.explicitDriver = true
		return nil
	}
}

// WithLibraryPath uses exactly this libkvlite file, without catalog fallback.
func WithLibraryPath(path string) Option {
	return func(c *config) error {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("%w: library path is required", ErrInvalidArgument)
		}
		c.libraryPath = path
		return nil
	}
}

// Open loads an installed KVLite native bundle and opens an embedded database.
// It does not compile or import the KVLite engine. Without WithDriver, the
// selected bundle's default driver is used.
func Open(path string, options ...Option) (*DB, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return nil, fmt.Errorf("%w: database path is required and cannot contain NUL", ErrInvalidArgument)
	}
	c := config{driver: "rocksdb"}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("%w: nil option", ErrInvalidArgument)
		}
		if err := option(&c); err != nil {
			return nil, err
		}
	}
	library, err := findLibrary(c)
	if err != nil {
		return nil, err
	}
	native, err := openNative(library, path, c.driver, c.explicitDriver)
	if err != nil {
		return nil, err
	}
	return &DB{native: native}, nil
}

// Put stores a JSON value. A zero TTL means no expiry. Positive subsecond TTLs
// round up to one second, matching the native ABI's seconds resolution.
func (db *DB) Put(key string, value any, ttl time.Duration) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return db.PutBytes([]byte(key), payload, ttl)
}

// Get decodes a JSON value into out.
func (db *DB) Get(key string, out any) error {
	payload, err := db.GetBytes([]byte(key))
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, out)
}

// PutBytes stores caller-serialized bytes without a Go JSON conversion.
func (db *DB) PutBytes(key, value []byte, ttl time.Duration) error {
	if db == nil || db.native == nil {
		return ErrClosed
	}
	if len(key) == 0 || ttl < 0 {
		return fmt.Errorf("%w: key must not be empty and TTL must not be negative", ErrInvalidArgument)
	}
	seconds := int64(ttl / time.Second)
	if ttl%time.Second != 0 {
		seconds++
	}
	return db.native.put(key, value, seconds)
}

// GetBytes returns caller-serialized bytes. Missing keys return ErrNotFound.
func (db *DB) GetBytes(key []byte) ([]byte, error) {
	if db == nil || db.native == nil {
		return nil, ErrClosed
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("%w: key must not be empty", ErrInvalidArgument)
	}
	return db.native.get(key)
}

// Delete removes a key. Deleting a missing key succeeds.
func (db *DB) Delete(key string) error { return db.DeleteBytes([]byte(key)) }

// DeleteBytes removes a byte key. Deleting a missing key succeeds.
func (db *DB) DeleteBytes(key []byte) error {
	if db == nil || db.native == nil {
		return ErrClosed
	}
	if len(key) == 0 {
		return fmt.Errorf("%w: key must not be empty", ErrInvalidArgument)
	}
	return db.native.delete(key)
}

// Close releases the database handle. It is safe to call more than once.
func (db *DB) Close() error {
	if db == nil || db.native == nil {
		return nil
	}
	return db.native.close()
}

func validDriver(name string) bool {
	if name == "" {
		return false
	}
	for index, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		if index == 0 || (r != '-' && r != '_') {
			return false
		}
	}
	return true
}

func findLibrary(c config) (string, error) {
	if c.libraryPath != "" {
		return requireLibrary(c.libraryPath)
	}
	if path := os.Getenv("KVLITE_LIBRARY_PATH"); path != "" {
		return requireLibrary(path)
	}
	name := libraryName()
	var roots []string
	if home := os.Getenv("KVLITE_HOME"); home != "" {
		roots = append(roots, home)
	}
	roots = append(roots, filepath.SplitList(os.Getenv("KVLITE_SYSTEM_MODULE_PATH"))...)
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".local", "lib", "kvlite"))
	}
	roots = append(roots, "/usr/local/lib/kvlite", "/usr/lib/kvlite")
	for _, root := range roots {
		if root == "" {
			continue
		}
		candidates := []string{filepath.Join(root, "drivers", c.driver, "lib", name)}
		if entries, err := os.ReadDir(filepath.Join(root, "drivers")); err == nil {
			var sole string
			for _, entry := range entries {
				path := filepath.Join(root, "drivers", entry.Name(), "lib", name)
				if regularFile(path) {
					if sole != "" {
						sole = ""
						break
					}
					sole = path
				}
			}
			if sole != "" {
				candidates = append(candidates, sole)
			}
		}
		candidates = append(candidates, filepath.Join(root, "lib", name))
		for _, candidate := range candidates {
			if regularFile(candidate) {
				return filepath.Abs(candidate)
			}
		}
	}
	return "", fmt.Errorf("%w: install a native driver bundle or set KVLITE_LIBRARY_PATH", ErrNativeLibrary)
}

func requireLibrary(path string) (string, error) {
	if !regularFile(path) {
		return "", fmt.Errorf("%w: %s is not a regular library file", ErrNativeLibrary, path)
	}
	return filepath.Abs(path)
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func libraryName() string {
	switch runtime.GOOS {
	case "darwin":
		return "libkvlite.dylib"
	case "windows":
		return "kvlite.dll"
	default:
		return "libkvlite.so"
	}
}
