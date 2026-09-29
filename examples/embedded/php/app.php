<?php

declare(strict_types=1);

require __DIR__.'/vendor/autoload.php';

use KVLite\KVLite;

$path = getenv('KVLITE_DB_PATH') ?: __DIR__.'/data';
$driver = getenv('KVLITE_DRIVER') ?: 'leveldb';
$db = KVLite::open($path, driver: $driver);

try {
    $db->put('user:101', ['id' => 101, 'name' => 'Ada'], ttlSeconds: 3600);
    echo 'user: '.json_encode($db->get('user:101'), JSON_THROW_ON_ERROR).PHP_EOL;

    $db->putBytes('blob:101', "\x00\x01\x02");
    echo 'bytes: '.bin2hex($db->getBytes('blob:101')).PHP_EOL;
} finally {
    $db->close();
}
