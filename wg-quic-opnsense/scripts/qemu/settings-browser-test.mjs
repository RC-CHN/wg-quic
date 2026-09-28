#!/usr/bin/env node
// Real DOM regression coverage. Dependencies are installed outside the repo;
// see TESTING.md. Native model/configd integration is exercised in the VM.
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const require = createRequire(path.join(process.env.WG_QUIC_BROWSER_MODULES || process.cwd(), 'package.json'));
const {chromium} = require('playwright-core');
const core = process.env.OPNSENSE_CORE_SRC;
if (!core) throw new Error('Set OPNSENSE_CORE_SRC to an official OPNsense core checkout');
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../');
const browser = await chromium.launch({executablePath: process.env.BROWSER_PATH, headless: true, args: ['--no-sandbox']});
try {
    const page = await browser.newPage();
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.route('http://fixture.test/', (route) => route.fulfill({contentType: 'text/html', body: `<div class="modal in" id="peer"><form id="frm_peer"><input id="client.name" value="draft"><table><tr><td><select id="client.servers" multiple><option value="a">A</option><option value="b">B</option></select></td></tr><tr><td><select id="client.fec_policy"><option value="balanced">balanced</option><option value="latency" selected>latency</option></select></td></tr></table></form></div>`}));
    const rows = [
        {uuid: 'a', name: 'A <script>', interface: 'quic0', enabled: true, congestion: 'auto', fec: 'off', obfs: 'salamander'},
        {uuid: 'b', name: 'B', interface: 'quic1', enabled: false, congestion: 'cubic', fec: 'auto', obfs: 'none'},
    ];
    let fail = false;
    let pending = null;
    await page.route('**/api/wireguardquic/client/list_servers', async (route) => {
        if (pending) { const callback = pending; pending = null; callback(route); return; }
        await route.fulfill({status: fail ? 500 : 200, json: {status: 'ok', rows}});
    });
    await page.goto('http://fixture.test/');
    for (const script of ['jquery-3.5.1.min.js', 'bootstrap.min.js', 'bootstrap-select.js', 'bootstrap-dialog.min.js', 'opnsense.js', 'opnsense_ui.js']) {
        await page.addScriptTag({path: path.join(core, 'src/opnsense/www/js', script)});
    }
    await page.addScriptTag({path: path.join(root, 'net/wg-quic/src/opnsense/www/js/wg-quic/settings.js')});
    await page.evaluate(() => WgQuicSettings.peerTransport('peer', Object.fromEntries([
        'title','scope','unassigned','unassignedPolicy','unavailable','unknownPolicy','instance','congestion','fec','obfs','edit','disabled','editNewTab','savedSettings','fecOff','fecMixed','fecActive','loading','retry',
    ].map((key) => [key, key]))));
    const select = (ids) => page.evaluate((ids) => $(document.getElementById('client.servers')).val(ids).trigger('change'), ids);
    const panel = page.locator('#peer-transport-settings');
    await select([]);
    await panel.getByText('unassigned', {exact: true}).waitFor();
    await select(['a']);
    await page.locator('#peer-fec-note').getByText('fecOff', {exact:true}).waitFor();
    assert.equal(await page.locator('[id="client.fec_policy"]').isDisabled(), true);
    assert.equal(await page.locator('[id="client.fec_policy"]').inputValue(), 'latency');
    assert.equal(await panel.locator('input,select,textarea').count(), 0);
    assert.equal(await panel.locator('tbody tr').count(), 1);
    assert.match(await panel.innerText(), /A <script>/);
    assert.equal(await panel.locator('script').count(), 0);
    assert.equal(await panel.locator('a').getAttribute('target'), '_blank');
    assert.match(await panel.locator('a').getAttribute('href'), /#instances&edit=a$/);
    await select(['a','b']);
    await page.locator('#peer-fec-note').getByText('fecMixed', {exact:true}).waitFor();
    assert.equal(await panel.locator('tbody tr').count(), 2);
    assert.equal(await page.locator('[id="client.fec_policy"]').isDisabled(), false);
    const saved = await page.evaluate(() => getFormData('frm_peer'));
    assert.equal(saved.client.fec_policy, 'latency');
    assert.deepEqual(Object.keys(saved.client).sort(), ['fec_policy','name','servers']);
    fail = true;
    await select(['b']);
    await panel.getByText('unavailable', {exact:true}).waitFor();
    assert.equal(await panel.locator('table').count(), 0);
    fail = false;
    await panel.getByRole('button', {name:'retry'}).click();
    await page.locator('#peer-fec-note').getByText('fecActive', {exact:true}).waitFor();
    // A slow response for an old choice must not replace the newer choice.
    let held;
    const caught = new Promise((resolve) => { pending = (route) => { held = route; resolve(); }; });
    await select(['a']);
    await caught;
    await select(['b']);
    await page.locator('#peer-fec-note').getByText('fecActive', {exact:true}).waitFor();
    await held.fulfill({json:{status:'ok',rows:[rows[0]]}});
    await page.waitForTimeout(100);
    assert.match(await panel.innerText(), /cubic/);
    assert.equal(await page.locator('[id="client.name"]').inputValue(), 'draft');
    // Saving, applying and restarting are separate, observable user actions.
    await page.evaluate(() => {
        $('body').append('<form id="frm_general_settings"><input id="general.enabled" value="1"></form><section><button id="reconfigureAct"></button><div id="change_message_base_form"></div></section>');
        WgQuicSettings.applyWorkflow(Object.fromEntries(['apply','applying','applied','restart_required','failed','unknown','cleanup','restart','restartWarning','cancel','query','saveFailed'].map((key) => [key, key])));
    });
    const requests = [];
    let saveOK = true;
    let applyState = 'restart_required';
    const report = (state) => ({uuid:'a', name:'A', interface:'quic0', peer_count:2, state, restart_token:'token', request_id:'original-request'});
    await page.route('**/api/wireguardquic/general/set', (route) => route.fulfill({json: saveOK ? {result:'saved'} : {result:'failed',validations:{'general.enabled':'invalid'}}}));
    await page.route('**/api/wireguardquic/service/**', async (route) => {
        requests.push({url:route.request().url(),body:route.request().postData()});
        const state = route.request().url().endsWith('/reconfigure') ? applyState : 'applied';
        await route.fulfill({json:{status:state === 'applied' ? 'ok' : 'attention',instances:[report(state)]}});
    });
    await page.locator('#reconfigureAct').click();
    const restart = page.getByRole('button', {name:'restart quic0',exact:true});
    await restart.waitFor();
    assert.equal(requests.length, 1);
    await restart.click();
    await page.getByRole('button', {name:'cancel',exact:true}).click();
    assert.equal(requests.length, 1, 'cancel must never restart');
    await restart.click();
    await page.locator('.bootstrap-dialog').getByRole('button', {name:'restart',exact:true}).click();
    await page.locator('#wg-quic-apply-results').getByText('applied',{exact:true}).waitFor();
    assert.match(requests[1].url, /restart_instance\/a$/);
    assert.equal(requests[1].body,'restart_token=token');
    applyState = 'unknown';
    await page.locator('#reconfigureAct').click();
    await page.getByRole('button',{name:'query',exact:true}).click();
    await page.locator('#wg-quic-apply-results').getByText('applied',{exact:true}).waitFor();
    assert.match(requests[3].url,/query_apply\/a$/);
    assert.equal(requests[3].body,'request_id=original-request');
    saveOK = false;
    await page.locator('#reconfigureAct').click();
    await page.locator('#wg-quic-apply-results').getByText('saveFailed',{exact:true}).waitFor();
    assert.equal(requests.length,4,'failed saves must not apply');
    for (const script of ['jquery.qrcode.js','qrcode.js']) {
        await page.addScriptTag({path:path.join(core, 'src/opnsense/www/js', script)});
    }
    await page.evaluate(() => {
        const names = ['endpoint','peer_dns','name','pubkey','privkey','address','psk','tunneladdress','keepalive'];
        const fields = names.map(name=>'<tr><td><input id="configbuilder.'+name+'"></td></tr>').join('');
        $('body').append('<form id="frm_config_builder"><table><tr><td><select id="configbuilder.servers"><option value="a">A</option><option value="b">B</option></select></td></tr>'+fields+'<tr><td><textarea id="configbuilder.output"></textarea></td><td></td><td></td></tr><tr><td><span id="configbuilder.store_btn"></span></td></tr></table></form><button id="btn_configbuilder_save"></button>');
        WgQuicSettings.configBuilder(Object.fromEntries(['store','next','choose','loading','loadFailed','noAddress','inherited','saved','saveFailed','unknown','discard','cancel'].map(key=>[key,key])));
        for (const name of ['pubkey','privkey']) $(document.getElementById('configbuilder.'+name)).val('fixture-key');
        $(document.getElementById('configbuilder.name')).val('draft-name');
        $(document.getElementById('configbuilder.tunneladdress')).val('0.0.0.0/0');
    });
    const info = {status:'ok', pubkey:'server-key', revision:'revision', address:'10.0.0.2/32', peer_dns:'1.1.1.1', endpoint:'example.com:51820', mtu:'1280', congestion:'model', fec:'off', obfs:'none'};
    let builderHeld, loadFail = false, builderSave = false;
    let catchBuilder;
    await page.route('**/api/wireguardquic/client/get_server_info/*', async route => {
        if (catchBuilder) { const callback=catchBuilder;catchBuilder=null;callback(route);return; }
        await route.fulfill({json:loadFail ? {status:'failed'} : info});
    });
    const builderRequests=[];
    await page.route('**/api/wireguardquic/client/add_client_builder', async route => {
        builderRequests.push(route.request().postData());
        await route.fulfill({json:builderSave ? {result:'saved',uuid:'created'} : {result:'failed'}});
    });
    const choose = id=>page.evaluate(id=>$(document.getElementById('configbuilder.servers')).val(id).trigger('change'),id);
    const config=page.locator('[id="configbuilder.output"]');
    const builder=page.locator('#btn_configbuilder_save');
    await choose('a');
    await page.waitForFunction(()=>document.getElementById('configbuilder.output').value.includes('obfs = none'));
    assert.match(await config.inputValue(),/congestion = model/);
    const caughtBuilder=new Promise(resolve=>{catchBuilder=route=>{builderHeld=route;resolve();};});
    await choose('a'); await caughtBuilder; await choose('b');
    await page.waitForFunction(()=>document.getElementById('configbuilder.output').value.includes('obfs = none'));
    await builderHeld.fulfill({json:{...info,pubkey:'WRONG',obfs:'salamander'}});
    await page.waitForTimeout(100);
    assert.ok(!(await config.inputValue()).includes('WRONG'));
    loadFail=true; await choose('a');
    await page.getByText('loadFailed',{exact:true}).waitFor();
    assert.equal(await config.inputValue(),'');
    assert.equal(await builder.isDisabled(),true);
    assert.equal(await page.locator('[id="configbuilder.name"]').inputValue(),'draft-name');
    loadFail=false; await choose('b');
    await page.waitForFunction(()=>document.getElementById('configbuilder.output').value.includes('obfs = none'));
    await page.locator('[id="configbuilder.endpoint"]').fill('override.example:443');
    const draft=await config.inputValue();
    await builder.click();
    await page.locator('#builder-notice').getByText('saveFailed',{exact:true}).waitFor();
    assert.equal(await config.inputValue(),draft);
    assert.equal(await builder.isDisabled(),false);
    builderSave=true; await builder.click();
    await page.getByText('saved',{exact:true}).waitFor();
    assert.equal(await config.inputValue(),draft);
    assert.equal(await builder.isDisabled(),true);
    assert.ok(builderRequests.every(body=>body.includes('revision')));
    assert.ok(!requests.some(request=>request.url.includes('/server/set_server')));
    // Starting the next peer is explicit; cancelling preserves private material.
    await page.getByRole('button',{name:'next',exact:true}).click();
    await page.locator('.bootstrap-dialog .btn').filter({hasText:'cancel'}).click();
    assert.equal(await config.inputValue(),draft);
    assert.deepEqual(errors, []);
    console.log('OPNsense inherited transport browser interactions passed');
} finally {
    await browser.close();
}
