import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { findLibrary } from '../src/index.js';

const catalog = fs.mkdtempSync(path.join(os.tmpdir(), 'kvlite-node-catalog-'));
try {
  const filename = process.platform === 'darwin' ? 'libkvlite.dylib' : process.platform === 'win32' ? 'kvlite.dll' : 'libkvlite.so';
  const installed = path.join(catalog, 'drivers', 'leveldb', 'lib', filename);
  fs.mkdirSync(path.dirname(installed), { recursive: true });
  fs.copyFileSync(process.env.KVLITE_TEST_LIBRARY, installed);
  const host = path.join(catalog, 'lib', filename);
  fs.mkdirSync(path.dirname(host), { recursive: true });
  fs.copyFileSync(process.env.KVLITE_TEST_LIBRARY, host);
  const previous = process.env.KVLITE_SYSTEM_MODULE_PATH;
  const previousLibrary = process.env.KVLITE_LIBRARY_PATH;
  const previousHome = process.env.KVLITE_HOME;
  process.env.KVLITE_SYSTEM_MODULE_PATH = catalog;
  delete process.env.KVLITE_LIBRARY_PATH;
  delete process.env.KVLITE_HOME;
  const preferHost = !(process.platform === 'darwin' && process.arch === 'x64');
  assert.equal(findLibrary(undefined, 'leveldb'), preferHost ? host : installed);
  if (previous === undefined) delete process.env.KVLITE_SYSTEM_MODULE_PATH; else process.env.KVLITE_SYSTEM_MODULE_PATH = previous;
  if (previousLibrary === undefined) delete process.env.KVLITE_LIBRARY_PATH; else process.env.KVLITE_LIBRARY_PATH = previousLibrary;
  if (previousHome === undefined) delete process.env.KVLITE_HOME; else process.env.KVLITE_HOME = previousHome;
} finally {
  fs.rmSync(catalog, { recursive: true, force: true });
}

console.log('Node installed-catalog discovery passed');
