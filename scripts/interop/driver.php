<?php
// 互通测试的 PHP 端：用官方 V1/V2 SDK 调本系统。由 internal/interop 的测试在 PHP 容器里调用。
// 官方 SDK 挂在 /sdk。从标准输入读 {"v1":{配置},"v2":{配置},"steps":[...]}，按顺序执行，结果以 JSON 数组输出。

function load_sdk_class($file, $as) {
    // 两个版本的 SDK 类同名，改个名再加载。
    $code = file_get_contents($file);
    $code = preg_replace('/^<\?php/', '', $code);
    $code = str_replace('class EpayCore', 'class ' . $as, $code);
    eval($code);
}
load_sdk_class('/sdk/v1/lib/EpayCore.class.php', 'EpayCoreV1');
load_sdk_class('/sdk/v2/SDK/lib/EpayCore.class.php', 'EpayCoreV2');

$in = json_decode(stream_get_contents(STDIN), true);
$v1 = new EpayCoreV1($in['v1']);
$v2 = new EpayCoreV2($in['v2']);

$out = [];
foreach ($in['steps'] as $s) {
    try {
        switch ($s['op']) {
            case 'v1.apiPay':      $r = $v1->apiPay($s['params']); break;
            case 'v1.getPayLink':  $r = $v1->getPayLink($s['params']); break;
            case 'v1.pagePay':     $r = $v1->pagePay($s['params']); break;
            case 'v1.queryOrder':  $r = $v1->queryOrder($s['trade_no']); break;
            case 'v1.orderStatus': $r = $v1->orderStatus($s['trade_no']); break;
            case 'v1.refund':      $r = $v1->refund($s['trade_no'], $s['money']); break;
            case 'v1.verifyNotify': $_GET = $s['params']; $r = $v1->verifyNotify(); break;
            case 'v1.verifyReturn': $_GET = $s['params']; $r = $v1->verifyReturn(); break;
            case 'v2.apiPay':      $r = $v2->apiPay($s['params']); break;
            case 'v2.getPayLink':  $r = $v2->getPayLink($s['params']); break;
            case 'v2.queryOrder':  $r = $v2->queryOrder($s['trade_no']); break;
            case 'v2.orderStatus': $r = $v2->orderStatus($s['trade_no']); break;
            case 'v2.refund':      $r = $v2->refund($s['out_refund_no'], $s['trade_no'], $s['money']); break;
            case 'v2.execute':     $r = $v2->execute($s['path'], $s['params']); break;
            case 'v2.verify':      $r = $v2->verify($s['params']); break;
            default: throw new Exception('未知操作 ' . $s['op']);
        }
        $out[] = ['ok' => true, 'result' => $r];
    } catch (Throwable $e) {
        $out[] = ['ok' => false, 'error' => $e->getMessage()];
    }
}
echo json_encode($out, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
