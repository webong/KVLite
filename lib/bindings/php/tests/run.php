<?php

declare(strict_types=1);

use Webong\KVLite\Exception\NotFoundException;
use Webong\KVLite\HttpDatabase;
use Webong\KVLite\KVLite;
use Webong\KVLite\Native\LibraryFinder;

$package = dirname(__DIR__);
spl_autoload_register(static function (string $class) use ($package): void {
    $prefix = 'Webong\\KVLite\\';
    if (!str_starts_with($class, $prefix)) {
        return;
    }
    $path = $package.'/src/'.str_replace('\\', '/', substr($class, strlen($prefix))).'.php';
    if (is_file($path)) {
        require $path;
    }
});

function check(bool $condition, string $message): void
{
    if (!$condition) {
        throw new RuntimeException($message);
    }
}

$library = $argv[1] ?? '';
check($library !== '', 'mock library path is required');

$catalog = sys_get_temp_dir().'/kvlite-php-catalog-'.bin2hex(random_bytes(6));
$installed = $catalog.'/drivers/leveldb/lib/'.basename($library);
check(mkdir(dirname($installed), 0700, true), 'cannot create test catalog');
check(copy($library, $installed), 'cannot copy test library');
$previousCatalog = getenv('KVLITE_SYSTEM_MODULE_PATH');
$previousLibrary = getenv('KVLITE_LIBRARY_PATH');
$previousHome = getenv('KVLITE_HOME');
putenv('KVLITE_SYSTEM_MODULE_PATH='.$catalog);
putenv('KVLITE_LIBRARY_PATH');
putenv('KVLITE_HOME');
check(LibraryFinder::find(driver: 'leveldb') === $installed, 'installed driver catalog was not discovered');
$previousCatalog === false ? putenv('KVLITE_SYSTEM_MODULE_PATH') : putenv('KVLITE_SYSTEM_MODULE_PATH='.$previousCatalog);
$previousLibrary === false ? putenv('KVLITE_LIBRARY_PATH') : putenv('KVLITE_LIBRARY_PATH='.$previousLibrary);
$previousHome === false ? putenv('KVLITE_HOME') : putenv('KVLITE_HOME='.$previousHome);
unlink($installed);
rmdir(dirname($installed));
rmdir(dirname(dirname($installed)));
rmdir(dirname(dirname(dirname($installed))));
rmdir($catalog);

$database = KVLite::open('/tmp/kvlite-php-mock', $library, driver: 'leveldb');
$database->put('user:101', ['id' => 101, 'name' => 'Ada'], 60);
check($database->get('user:101') === ['id' => 101, 'name' => 'Ada'], 'native JSON round trip failed');
$database->putBytes("binary\0key", "value\0bytes");
check($database->getBytes("binary\0key") === "value\0bytes", 'native binary round trip failed');
$database->delete("binary\0key");
try {
    $database->getBytes("binary\0key");
    throw new RuntimeException('missing key did not throw');
} catch (NotFoundException) {
}
$database->close();

// `backend` remains an embedded-call compatibility alias.
KVLite::open('/tmp/kvlite-php-legacy-backend', $library, 'leveldb')->close();

$requests = [];
$remote = new HttpDatabase('http://127.0.0.1:8089', 'secret', 5, static function (string $method, string $url, ?string $body, array $headers) use (&$requests): array {
    $requests[] = [$method, $url, $body, $headers];
    return match ($method) {
        'PUT', 'DELETE' => [204, ''],
        'GET' => [200, '{"enabled":true}'],
    };
}, 'leveldb');
$remote->put('flags:101', ['enabled' => true], 30);
check($remote->get('flags:101') === ['enabled' => true], 'HTTP JSON round trip failed');
$remote->delete('flags:101');
check(count($requests) === 3, 'HTTP request count mismatch');
check(str_contains($requests[0][1], 'ttl_seconds=30'), 'HTTP TTL was not sent');
check(in_array('Authorization: Bearer secret', $requests[0][3], true), 'HTTP token was not sent');
check(in_array('X-KVLite-Driver: leveldb', $requests[0][3], true), 'HTTP driver was not sent');

fwrite(STDOUT, "PHP binding tests passed\n");
