<?php
// 用官方服务端和官方 SDK 的签名代码生成测试向量，输出 JSON 到标准输出。
// 由 scripts/sign-vectors.sh 在 PHP 容器里调用：官方服务端源码挂在 /epay，官方 SDK 挂在 /sdk。
// RSA 密钥每次现生成，不用 SDK 里的样例密钥。

require '/epay/includes/functions.php';
require '/epay/includes/lib/Payment.php';

function load_sdk_class($file, $as) {
    // 两个版本的 SDK 类同名，改个名再加载。
    $code = file_get_contents($file);
    $code = preg_replace('/^<\?php/', '', $code);
    $code = str_replace('class EpayCore', 'class ' . $as, $code);
    eval($code);
}
load_sdk_class('/sdk/v1/lib/EpayCore.class.php', 'EpayCoreV1');
load_sdk_class('/sdk/v2/SDK/lib/EpayCore.class.php', 'EpayCoreV2');

function call_private($objOrClass, $method, ...$args) {
    $m = new ReflectionMethod($objOrClass, $method);
    $m->setAccessible(true);
    return $m->invoke(is_object($objOrClass) ? $objOrClass : null, ...$args);
}

function pem_body($pem) {
    return preg_replace('/-----[^-]+-----|\s+/', '', $pem);
}

$key = 'JibsFLkeuluG7AFLnK3DDPYa1m0WRYvT';

$md5Cases = [
    'V1 下单' => [
        'pid' => '1001', 'type' => 'alipay', 'out_trade_no' => '20261015123456', 'notify_url' => 'https://shop.example.com/notify',
        'return_url' => 'https://shop.example.com/return', 'name' => 'VIP 会员', 'money' => '10.00', 'sign_type' => 'MD5',
    ],
    '空值、0 和空白' => [
        'pid' => '1001', 'param' => '', 'cid' => '0', 'device' => ' ', 'clientip' => "\t", 'money' => '0.01', 'name' => 'x',
    ],
    '特殊字符原样拼接' => [
        'pid' => '1001', 'name' => 'a&b=c+d%20e f', 'param' => '{"uid":1,"note":"中文"}', 'notify_url' => 'https://a.example.com/n?x=1&y=2',
    ],
    '键名按字节序' => [
        'B' => '1', 'a' => '2', '_x' => '3', 'a_b' => '4', 'ab' => '5', 'Z' => '6', 'sign' => 'old', 'sign_type' => 'MD5',
    ],
    '通知参数' => [
        'pid' => '1001', 'trade_no' => '2026101509073391927', 'out_trade_no' => 'T1792026453', 'type' => 'alipay',
        'name' => '测试商品', 'money' => '10.00', 'trade_status' => 'TRADE_SUCCESS',
    ],
    '多字节和表情' => [
        'pid' => '1001', 'name' => '充值💰 100 点（含税）', 'money' => '100',
    ],
];

$out = ['md5_key' => $key, 'md5' => [], 'rsa' => [], 'values' => []];

foreach ($md5Cases as $title => $p) {
    $content = call_private('\lib\Payment', 'getSignContent', $p);
    $sign = \lib\Payment::makeSign($p, $key);
    $p2 = $p; $p2['sign'] = $sign;
    $sdk1 = new EpayCoreV1(['pid' => '1001', 'key' => $key, 'apiurl' => 'http://127.0.0.1/']);
    $out['md5'][] = [
        'title' => $title,
        'params' => (object)$p,
        'content' => $content,
        'sign' => $sign,
        'server_verify' => \lib\Payment::verifySign($p2, $key, ''),
        // 官方 V1 SDK 的 getSign 只跳过空串，不跳过纯空白，所以带纯空白的参数时和服务端不一致。
        'sdk_v1_sign' => call_private($sdk1, 'getSign', $p),
    ];
}

$res = openssl_pkey_new(['private_key_bits' => 2048, 'private_key_type' => OPENSSL_KEYTYPE_RSA]);
openssl_pkey_export($res, $privPem);
$pubPem = openssl_pkey_get_details($res)['key'];
$priv = pem_body($privPem);
$pub = pem_body($pubPem);
$out['rsa_private_key'] = $priv;
$out['rsa_public_key'] = $pub;

// 服务端用全局 $conf['private_key'] 做平台签名；SDK 用商户私钥签名。两者算法相同。
$GLOBALS['conf'] = ['private_key' => $priv];
$sdk2 = new EpayCoreV2(['apiurl' => 'http://127.0.0.1/', 'pid' => '1001', 'platform_public_key' => $pub, 'merchant_private_key' => $priv]);

$rsaCases = [
    'V2 下单' => [
        'pid' => '1001', 'type' => 'alipay', 'out_trade_no' => 'V2-0001', 'notify_url' => 'https://shop.example.com/notify',
        'return_url' => 'https://shop.example.com/return', 'name' => '月卡', 'money' => '29.90', 'clientip' => '203.0.113.7',
        'timestamp' => '1792026453', 'sign_type' => 'RSA',
    ],
    'V2 通知' => [
        'pid' => '1001', 'trade_no' => '2026101509073391927', 'out_trade_no' => 'V2-0001', 'type' => 'alipay', 'name' => '月卡',
        'money' => '29.90', 'trade_status' => 'TRADE_SUCCESS', 'api_trade_no' => '2026101522001400000000000001',
        'buyer' => '张*(zha***@example.com)', 'param' => '0', 'timestamp' => '1792026500', 'sign_type' => 'RSA',
    ],
    'V2 空值' => [
        'pid' => '1001', 'trade_no' => '', 'out_trade_no' => 'x y', 'device' => ' ', 'timestamp' => '1792026453',
    ],
];
foreach ($rsaCases as $title => $p) {
    $content = call_private('\lib\Payment', 'getSignContent', $p);
    $p['sign_type'] = 'RSA';
    $sign = \lib\Payment::makeSign($p, '');
    $p2 = $p; $p2['sign'] = $sign;
    $out['rsa'][] = [
        'title' => $title,
        'params' => (object)$p,
        'content' => $content,
        'sign' => $sign,
        'server_verify' => \lib\Payment::verifySign($p2, '', $pub),
        'sdk_v2_content' => call_private($sdk2, 'getSignContent', $p),
        'sdk_v2_sign' => call_private($sdk2, 'rsaPrivateSign', $content),
    ];
}

// 接口响应：值是 JSON 解码后的类型（整数、字符串、null、数组），官方 V2 SDK 用 getSignContent 验签。
$values = [
    '查单响应' => '{"code":0,"trade_no":"2026101509073391927","out_trade_no":"V2-0001","type":"alipay","pid":1001,"addtime":"2026-10-15 09:07:33","name":"月卡","money":"29.90","status":1,"timestamp":"1792026453","sign_type":"RSA"}',
    '带空值和数组' => '{"code":0,"msg":"","list":[1,2],"obj":{"a":1},"none":null,"zero":0,"neg":-1,"timestamp":"1792026453"}',
];
foreach ($values as $title => $json) {
    $arr = json_decode($json, true);
    $content = call_private($sdk2, 'getSignContent', $arr);
    $out['values'][] = [
        'title' => $title,
        'json' => $json,
        'content' => $content,
        'sign' => call_private($sdk2, 'rsaPrivateSign', $content),
    ];
}

echo json_encode($out, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_PRETTY_PRINT), "\n";
