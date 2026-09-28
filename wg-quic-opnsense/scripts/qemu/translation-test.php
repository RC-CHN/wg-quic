<?php
// SPDX-License-Identifier: GPL-3.0-or-later
// Run inside the disposable OPNsense guest after installing the plugin.
require '/usr/local/opnsense/mvc/app/library/OPNsense/WireguardQuic/Translator.php';
function view_html_safe($value) { return htmlspecialchars($value, ENT_QUOTES); }
function check($value, $message) { if (!$value) throw new Exception($message); }
$t = new OPNsense\WireguardQuic\Translator('zh_CN');
check($t->text('Congestion control') === '拥塞控制', 'Chinese vocabulary');
check($t->_('<script>') === '&lt;script&gt;', 'HTML escaping');
$input = ['client' => ['name' => 'Automatic', 'servers' => [['value' => 'Automatic', 'selected' => 1]], 'fec_policy' => ['balanced' => ['value' => 'Balanced', 'selected' => 1]]], 'validations' => ['client.name' => 'Choose a peer.']];
$result = $t->response($input);
check($result['client']['name'] === 'Automatic' && $result['client']['servers'][0]['value'] === 'Automatic', 'user text remains untouched');
check($result['client']['fec_policy']['balanced']['value'] === '均衡' && $result['client']['fec_policy']['balanced']['selected'] === 1, 'option label only');
check($result['validations']['client.name'] === '请选择一个对端。', 'validation translated');
check((new OPNsense\WireguardQuic\Translator('en_US'))->response($input) === $input, 'English fallback');
check((new OPNsense\WireguardQuic\Translator('zh_TW'))->text('Congestion control') === 'Congestion control', 'unsupported locale fallback');
echo "PHP translation isolation passed\n";
