import { open } from 'usekvlite';
import { fileURLToPath } from 'node:url';

const path = process.env.KVLITE_DB_PATH ?? fileURLToPath(new URL('./data', import.meta.url));
const driver = process.env.KVLITE_DRIVER ?? 'leveldb';
const db = open(path, { driver });

try {
  db.put('user:101', { id: 101, name: 'Ada' }, { ttlSeconds: 3600 });
  console.log('user:', db.get('user:101'));

  db.putBytes('blob:101', Buffer.from([0, 1, 2]));
  console.log('bytes:', db.getBytes('blob:101').toString('hex'));
} finally {
  db.close();
}
